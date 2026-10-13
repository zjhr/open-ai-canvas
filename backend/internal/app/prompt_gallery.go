package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"yingce/backend/internal/model"
	"yingce/backend/internal/outbound"
	"yingce/backend/internal/repository"
)

const (
	PromptGallerySyncLimitAll = 0
	promptGallerySyncLimitMax = 50000
)

// PromptGallerySourceView 是画廊远端来源的状态（前端展示 + 决策是否可用）。
type PromptGallerySourceView struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	License      string     `json:"license"`
	Revision     string     `json:"revision"`
	TotalRows    int64      `json:"totalRows"`
	LastSyncedAt *time.Time `json:"lastSyncedAt"`
	Running      bool       `json:"running"`
	LastError    string     `json:"lastError,omitempty"`
}

type PromptGallerySearchRequest struct {
	Keyword  string `json:"keyword"`
	Mode     string `json:"mode"`
	SourceID string `json:"sourceId"`
	Category string `json:"category"`
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
}

type PromptGalleryPromptView struct {
	ID         string `json:"id"`
	SourceID   string `json:"sourceId"`
	Mode       string `json:"mode"`
	Title      string `json:"title"`
	Description string `json:"description"`
	Prompt     string `json:"prompt"`
	PromptZh   string `json:"promptZh,omitempty"`
	Category   string `json:"category"`
	Model      string `json:"model"`
	Credit     string `json:"credit"`
	SourceURL  string `json:"sourceUrl"`
	CoverPath  string `json:"coverPath,omitempty"`
	Ratio      string `json:"ratio"`
}

type PromptGalleryFacetView struct {
	ID    string `json:"id"`
	Count int64  `json:"count"`
}

type PromptGallerySearchResult struct {
	Items      []PromptGalleryPromptView  `json:"items"`
	Total      int64                      `json:"total"`
	Sources    []PromptGalleryFacetView   `json:"sources"`
	Categories []PromptGalleryFacetView   `json:"categories"`
}

// promptGallerySourceDescriptor 描述一个可同步的远端提示词来源。
// 只有两个 HF JSONL 数据集走服务端索引；站内静态目录仍由前端灵感库提供。
type promptGallerySourceDescriptor struct {
	ID           string
	Name         string
	License      string
	Dataset      string
	MetadataPath string
	Mode         string
	CoverBaseURL string
	// CoverPrefix 是封面相对路径必须命中的目录前缀：挡掉把接口当任意地址代理使用。
	CoverPrefix string
}

var promptGallerySources = []promptGallerySourceDescriptor{
	{
		ID:           "hf-seedance-2",
		Name:         "Seedance 2 提示词数据集",
		License:      "CC BY 4.0",
		Dataset:      "GokuScraper/seedance-2-prompts-datasets",
		MetadataPath: "metadata.jsonl",
		Mode:         "video",
		CoverBaseURL: "https://huggingface.co/datasets/GokuScraper/seedance-2-prompts-datasets/resolve/main",
		CoverPrefix:  "seedance-2/covers/",
	},
	{
		ID:           "hf-gpt-image-2",
		Name:         "GPT Image 2 提示词数据集",
		License:      "CC BY 4.0",
		Dataset:      "Goku-OpenLab/gpt-image-2-prompts-datasets",
		MetadataPath: "metadata.jsonl",
		Mode:         "image",
		CoverBaseURL: "https://huggingface.co/datasets/Goku-OpenLab/gpt-image-2-prompts-datasets/resolve/main",
		CoverPrefix:  "gpt-image-2/images/",
	},
}

func promptGallerySourceByID(sourceID string) (promptGallerySourceDescriptor, bool) {
	for _, source := range promptGallerySources {
		if source.ID == sourceID {
			return source, true
		}
	}
	return promptGallerySourceDescriptor{}, false
}

// PromptGallerySources 返回所有远端来源的当前状态（库内条数 + 后台同步状态）。
func (s *Service) PromptGallerySources() ([]PromptGallerySourceView, error) {
	views := make([]PromptGallerySourceView, 0, len(promptGallerySources))
	rows, err := s.repo.PromptGallerySources()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]model.PromptGallerySource, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}

	s.promptGalleryMu.Lock()
	defer s.promptGalleryMu.Unlock()
	for _, descriptor := range promptGallerySources {
		view := PromptGallerySourceView{ID: descriptor.ID, Name: descriptor.Name, License: descriptor.License}
		if row, ok := byID[descriptor.ID]; ok {
			view.Revision = row.Revision
			view.TotalRows = row.TotalRows
			view.LastSyncedAt = row.LastSyncedAt
		}
		view.Running = s.promptGalleryRunning[descriptor.ID]
		view.LastError = s.promptGalleryErr[descriptor.ID]
		views = append(views, view)
	}
	return views, nil
}

// StartPromptGallerySync 异步开始一个来源的同步；同步状态可从 PromptGallerySources 查询。
func (s *Service) StartPromptGallerySync(sourceID string) error {
	if _, ok := promptGallerySourceByID(sourceID); !ok {
		return fmt.Errorf("未知的提示词画廊来源: %s", sourceID)
	}
	s.promptGalleryMu.Lock()
	if s.promptGalleryRunning[sourceID] {
		s.promptGalleryMu.Unlock()
		return fmt.Errorf("来源 %s 正在同步中", sourceID)
	}
	s.promptGalleryRunning[sourceID] = true
	delete(s.promptGalleryErr, sourceID)
	s.promptGalleryMu.Unlock()

	go func() {
		_, err := s.SyncPromptGallerySource(sourceID, PromptGallerySyncLimitAll)
		s.promptGalleryMu.Lock()
		s.promptGalleryRunning[sourceID] = false
		if err != nil {
			s.promptGalleryErr[sourceID] = err.Error()
			slog.Error("prompt gallery sync failed", "source", sourceID, "err", err)
		} else {
			delete(s.promptGalleryErr, sourceID)
		}
		s.promptGalleryMu.Unlock()
	}()
	return nil
}

// SyncPromptGallerySource 同步一个来源的 JSONL 元数据到本地索引。
// limit<=0 表示全量；limit>0 只解析前 limit 行，用于冒烟与测试。
func (s *Service) SyncPromptGallerySource(sourceID string, limit int) (PromptGallerySyncResult, error) {
	descriptor, ok := promptGallerySourceByID(sourceID)
	if !ok {
		return PromptGallerySyncResult{}, fmt.Errorf("未知的提示词画廊来源: %s", sourceID)
	}
	if s.repo == nil {
		return PromptGallerySyncResult{}, fmt.Errorf("提示词画廊索引需要数据库")
	}

	url := fmt.Sprintf("https://huggingface.co/datasets/%s/resolve/main/%s", descriptor.Dataset, descriptor.MetadataPath)
	if _, err := outbound.ValidateOutboundURL(url); err != nil {
		return PromptGallerySyncResult{}, fmt.Errorf("同步 URL 校验失败: %w", err)
	}
	client := outbound.OutboundHTTPClient(20 * time.Minute)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return PromptGallerySyncResult{}, err
	}
	req.Header.Set("User-Agent", "yingce-prompt-gallery-sync/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return PromptGallerySyncResult{}, fmt.Errorf("下载 %s 失败: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return PromptGallerySyncResult{}, fmt.Errorf("下载 %s 失败: HTTP %d", url, resp.StatusCode)
	}

	imported, syncedAt, err := s.importPromptGalleryJSONL(descriptor, resp.Body, limit)
	if err != nil {
		return PromptGallerySyncResult{}, err
	}

	source := model.PromptGallerySource{
		ID:           sourceID,
		Name:         descriptor.Name,
		License:      descriptor.License,
		Revision:     "hf-resolve-main",
		TotalRows:    imported,
		LastSyncedAt: &syncedAt,
	}
	if err := s.repo.UpsertPromptGallerySource(&source); err != nil {
		return PromptGallerySyncResult{}, fmt.Errorf("记录同步状态失败: %w", err)
	}
	slog.Info("prompt gallery synced", "source", sourceID, "rows", imported)
	return PromptGallerySyncResult{SourceID: sourceID, Rows: imported, SyncedAt: syncedAt}, nil
}

/**
 * 解析并落库一批 JSONL。
 *
 * 清理过期行必须用"写入这一批时用的同一个时间戳"：如果这里再取一次 now，
 * 刚写进去的行也满足 synced_at < now，整轮同步会被自己删空（写入 0 行却报成功）。
 */
func (s *Service) importPromptGalleryJSONL(descriptor promptGallerySourceDescriptor, body io.Reader, limit int) (int64, time.Time, error) {
	syncedAt := time.Now().UTC()
	rows, err := s.parsePromptGalleryJSONL(descriptor, body, limit, syncedAt)
	if err != nil {
		return 0, syncedAt, err
	}
	if err := s.repo.UpsertPromptGalleryPrompts(rows); err != nil {
		return 0, syncedAt, fmt.Errorf("写入提示词索引失败: %w", err)
	}
	if err := s.repo.DeletePromptGalleryPromptsStale(descriptor.ID, syncedAt); err != nil {
		return 0, syncedAt, fmt.Errorf("清理过期提示词索引失败: %w", err)
	}
	return int64(len(rows)), syncedAt, nil
}

type PromptGallerySyncResult struct {
	SourceID string    `json:"sourceId"`
	Rows     int64     `json:"rows"`
	SyncedAt time.Time `json:"syncedAt"`
}

// parsePromptGalleryJSONL 逐行解析 metadata.jsonl。两个 HF 数据集共用同一套 schema。
// limit<=0 全量；否则只读前 limit 行（仍把数据流读完，避免对长连接造成半开状态）。
func (s *Service) parsePromptGalleryJSONL(descriptor promptGallerySourceDescriptor, body io.Reader, limit int, syncedAt time.Time) ([]model.PromptGalleryPrompt, error) {
	scanner := bufio.NewScanner(body)
	// 单行 JSON 可能包含整段长正文，默认缓冲只有 64KB，必须放大。
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)

	rows := make([]model.PromptGalleryPrompt, 0, 2048)
	line := 0
	for scanner.Scan() {
		line++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" || !strings.HasPrefix(raw, "{") {
			continue
		}
		if limit > 0 && len(rows) >= limit {
			break
		}
		var item promptGalleryRawItem
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			slog.Warn("prompt gallery sync: skip invalid row", "source", descriptor.ID, "line", line, "err", err)
			continue
		}
		if item.ID == "" || item.PromptText() == "" {
			continue
		}
		rows = append(rows, mapPromptGalleryRow(descriptor, item, syncedAt))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", descriptor.MetadataPath, err)
	}
	return rows, nil
}

// promptGalleryRawItem 是 HF 两个数据集 JSONL 行的最小结构（多余字段忽略）。
type promptGalleryRawItem struct {
	ID        string `json:"id"`
	Category  string `json:"category"`
	IsFeatured bool  `json:"is_featured"`
	ModelInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"model_info"`
	I18N map[string]struct {
		T    string   `json:"t"`
		P    string   `json:"p"`
		Tags []string `json:"tags"`
	} `json:"i18n"`
	Media struct {
		C      string   `json:"c"`
		V      string   `json:"v"`
		Images []string `json:"images"`
	} `json:"media"`
	SourceLink string `json:"sourceLink"`
	Spec       struct {
		Duration float64 `json:"duration"`
		Width    int     `json:"width"`
		Height   int     `json:"height"`
		Ratio    float64 `json:"ratio"`
	} `json:"spec"`
}

func (item promptGalleryRawItem) PromptText() string {
	if item.I18N == nil {
		return ""
	}
	return strings.TrimSpace(item.I18N["en"].P + item.I18N["zh"].P)
}

func mapPromptGalleryRow(descriptor promptGallerySourceDescriptor, item promptGalleryRawItem, syncedAt time.Time) model.PromptGalleryPrompt {
	zh := item.I18N["zh"]
	en := item.I18N["en"]

	title := zh.T
	if title == "" {
		title = en.T
	}
	description := strings.Join(zh.Tags, " · ")
	if len(description) > 200 {
		description = description[:197] + "…"
	}
	prompt := strings.TrimSpace(en.P)
	promptZh := strings.TrimSpace(zh.P)

	modelName := strings.TrimSpace(item.ModelInfo.Name + " " + item.ModelInfo.Version)
	tags := append(append([]string{}, en.Tags...), zh.Tags...)
	tags = append(tags, modelName)
	credit := "HuggingFace 数据集" // 原帖作者未单列，来源链接指向上游 post

	ratio := ""
	if item.Spec.Width > 0 && item.Spec.Height > 0 {
		ratio = fmt.Sprintf("%dx%d", item.Spec.Width, item.Spec.Height)
	}

	coverPath := item.Media.C
	if coverPath == "" && len(item.Media.Images) > 0 {
		coverPath = item.Media.Images[0]
	}

	return model.PromptGalleryPrompt{
		ID:         descriptor.ID + ":" + item.ID,
		SourceID:   descriptor.ID,
		ExternalID: item.ID,
		Mode:       descriptor.Mode,
		Title:      strings.TrimSpace(title),
		Description: strings.TrimSpace(description),
		Prompt:     prompt,
		PromptZh:   promptZh,
		Category:   strings.TrimSpace(item.Category),
		Model:      modelName,
		Tags:       strings.Join(tags, " "),
		Credit:     credit,
		SourceURL:  strings.TrimSpace(item.SourceLink),
		CoverPath:  coverPath,
		Ratio:      ratio,
		Featured:   item.IsFeatured,
		SourceRevision: "hf-resolve-main",
		SyncedAt:   syncedAt,
	}
}

// PromptGallerySearch 检索远端索引，并把来源/分类 facet 一起返回。
func (s *Service) PromptGallerySearch(req PromptGallerySearchRequest) (PromptGallerySearchResult, error) {
	rows, total, err := s.repo.SearchPromptGalleryPrompts(repository.PromptGallerySearchQuery{
		Keyword:  req.Keyword,
		Mode:     req.Mode,
		SourceID: req.SourceID,
		Category: req.Category,
		Limit:    req.Limit,
		Offset:   req.Offset,
	})
	if err != nil {
		return PromptGallerySearchResult{}, err
	}
	items := make([]PromptGalleryPromptView, 0, len(rows))
	for _, row := range rows {
		items = append(items, PromptGalleryPromptView{
			ID:          row.ID,
			SourceID:    row.SourceID,
			Mode:        row.Mode,
			Title:       row.Title,
			Description: row.Description,
			Prompt:      row.Prompt,
			PromptZh:    row.PromptZh,
			Category:    row.Category,
			Model:       row.Model,
			Credit:      row.Credit,
			SourceURL:   row.SourceURL,
			CoverPath:   row.CoverPath,
			Ratio:       row.Ratio,
		})
	}

	result := PromptGallerySearchResult{Items: items, Total: total}
	sources, categories, err := s.repo.PromptGalleryFacets()
	if err != nil {
		return result, err
	}
	for _, source := range sources {
		result.Sources = append(result.Sources, PromptGalleryFacetView{ID: source.Key, Count: source.Count})
	}
	for _, category := range categories {
		result.Categories = append(result.Categories, PromptGalleryFacetView{ID: category.Key, Count: category.Count})
	}
	return result, nil
}