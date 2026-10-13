package repository

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"yingce/backend/internal/model"
)

// PromptGallerySearchQuery 是画廊检索的条件；标题/正文/标签/模型走 LIKE，分页按来源等过滤。
type PromptGallerySearchQuery struct {
	Keyword     string
	Mode        string
	SourceID    string
	Category    string
	HasFeatured bool
	Featured    bool
	Limit       int
	Offset      int
}

// FacetCount 是某个维度（来源/分类）的计数。
type FacetCount struct {
	Key   string `gorm:"column:key"`
	Count int64  `gorm:"column:count"`
}

/**
 * 单条 upsert 语句的最大行数。
 *
 * SQLite 对一条语句的绑定变量数量有上限（现代版本 32766，旧版 999），
 * 8 千多条一次性插入会直接报 "too many SQL variables"，所以按批切分。
 * 200 行 × 20 列 = 4000 个变量，两种数据库都有余量。
 */
const promptGalleryUpsertBatch = 200

// UpsertPromptGalleryPrompts 批量写入从同步流解析出的条目（按 source_id + external_id 幂等）。
func (r *Repository) UpsertPromptGalleryPrompts(rows []model.PromptGalleryPrompt) error {
	if len(rows) == 0 {
		return nil
	}
	for start := 0; start < len(rows); start += promptGalleryUpsertBatch {
		end := start + promptGalleryUpsertBatch
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[start:end]
		if err := r.db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "source_id"}, {Name: "external_id"}},
			UpdateAll: true,
		}).Create(&batch).Error; err != nil {
			return fmt.Errorf("写入提示词索引第 %d-%d 行失败: %w", start+1, end, err)
		}
	}
	return nil
}

// DeletePromptGalleryPromptsStale 删除某来源在某个同步时间之前存在的、但本次未更新的行。
func (r *Repository) DeletePromptGalleryPromptsStale(sourceID string, syncedAt time.Time) error {
	return r.db.Where("source_id = ? AND synced_at < ?", sourceID, syncedAt).Delete(&model.PromptGalleryPrompt{}).Error
}

// SearchPromptGalleryPrompts 分页检索，返回命中行与总数。
//
// SQLite/PostgreSQL 的 LIKE 语义一致（ASCII 大小写不敏感、中文直接包含匹配），
// 近三万条文本行全表扫描在两种数据库都是毫秒级，因此暂不引入 FTS/tsvector 的跨库抽象。
// 用户输入里的 % 与 _ 按普通通配符处理，不额外转义。
func (r *Repository) SearchPromptGalleryPrompts(query PromptGallerySearchQuery) ([]model.PromptGalleryPrompt, int64, error) {
	apply := func(db *gorm.DB) *gorm.DB {
		if keyword := trimSpace(query.Keyword); keyword != "" {
			like := "%" + keyword + "%"
			db = db.Where("(title LIKE ? OR description LIKE ? OR prompt LIKE ? OR prompt_zh LIKE ? OR tags LIKE ? OR model LIKE ?)",
				like, like, like, like, like, like)
		}
		if query.Mode != "" {
			db = db.Where("mode = ?", query.Mode)
		}
		if query.SourceID != "" {
			db = db.Where("source_id = ?", query.SourceID)
		}
		if query.Category != "" {
			db = db.Where("category = ?", query.Category)
		}
		if query.HasFeatured {
			db = db.Where("featured = ?", query.Featured)
		}
		return db
	}

	var total int64
	if err := apply(r.db.Model(&model.PromptGalleryPrompt{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	limit := query.Limit
	if limit <= 0 || limit > 200 {
		limit = 24
	}
	offset := query.Offset
	if offset < 0 {
		offset = 0
	}
	var rows []model.PromptGalleryPrompt
	if err := apply(r.db.Model(&model.PromptGalleryPrompt{})).
		Order("featured desc, id asc").
		Limit(limit).Offset(offset).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// PromptGalleryFacets 返回来源与分类的计数，供前端筛选项渲染。
func (r *Repository) PromptGalleryFacets() ([]FacetCount, []FacetCount, error) {
	var sources, categories []FacetCount
	if err := r.db.Model(&model.PromptGalleryPrompt{}).Select("source_id as key, count(*) as count").Group("source_id").Scan(&sources).Error; err != nil {
		return nil, nil, err
	}
	if err := r.db.Model(&model.PromptGalleryPrompt{}).Select("category as key, count(*) as count").Where("category <> ''").Group("category").Order("count desc").Scan(&categories).Error; err != nil {
		return nil, nil, err
	}
	return sources, categories, nil
}

// PromptGallerySources 返回全部来源的同步状态。
func (r *Repository) PromptGallerySources() ([]model.PromptGallerySource, error) {
	var rows []model.PromptGallerySource
	err := r.db.Order("id asc").Find(&rows).Error
	return rows, err
}

// UpsertPromptGallerySource 记录/更新某个来源的同步状态。
func (r *Repository) UpsertPromptGallerySource(row *model.PromptGallerySource) error {
	return r.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(row).Error
}

func trimSpace(value string) string {
	start := 0
	for start < len(value) && value[start] <= ' ' {
		start++
	}
	end := len(value)
	for end > start && value[end-1] <= ' ' {
		end--
	}
	return value[start:end]
}