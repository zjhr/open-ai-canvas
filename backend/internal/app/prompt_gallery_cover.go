package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yingce/backend/internal/outbound"
)

/** 单张封面最大字节数。上游封面 100KB～2MB，8MB 足够覆盖异常样本。 */
const promptGalleryCoverMaxBytes int64 = 8 << 20

// PromptGalleryCover 是代理读取到的封面（已落盘，Body 由调用方关闭）。
type PromptGalleryCover struct {
	Path     string
	MimeType string
	Size     int64
	Body     io.ReadCloser
}

/**
 * OpenPromptGalleryCover 按需读取远端封面并缓存到数据目录。
 *
 * 浏览器不直连 HuggingFace：一是跨域与防盗链，二是封面路径来自数据库、
 * 必须按来源白名单校验，避免把接口变成任意地址代理。
 */
func (s *Service) OpenPromptGalleryCover(sourceID string, coverPath string) (*PromptGalleryCover, error) {
	descriptor, ok := promptGallerySourceByID(sourceID)
	if !ok {
		return nil, BadAuthRequest("未知的提示词画廊来源")
	}
	clean, err := promptGalleryCoverRelativePath(descriptor, coverPath)
	if err != nil {
		return nil, err
	}

	cacheDir := filepath.Join(s.dataDir, "prompt-gallery-covers", descriptor.ID)
	cachePath := filepath.Join(cacheDir, promptGalleryCoverCacheName(clean))
	if stat, err := os.Stat(cachePath); err == nil && stat.Size() > 0 {
		file, err := os.Open(cachePath)
		if err != nil {
			return nil, err
		}
		return &PromptGalleryCover{Path: cachePath, MimeType: promptGalleryCoverMime(cachePath), Size: stat.Size(), Body: file}, nil
	}

	url := fmt.Sprintf("%s/%s", descriptor.CoverBaseURL, clean)
	if _, err := outbound.ValidateOutboundURL(url); err != nil {
		return nil, fmt.Errorf("封面地址校验失败: %w", err)
	}
	client := outbound.OutboundHTTPClient(60 * time.Second)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "yingce-prompt-gallery/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("读取封面失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("读取封面失败: HTTP %d", resp.StatusCode)
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, err
	}
	temp, err := os.CreateTemp(cacheDir, "cover-*.tmp")
	if err != nil {
		return nil, err
	}
	tempPath := temp.Name()
	written, copyErr := io.Copy(temp, io.LimitReader(resp.Body, promptGalleryCoverMaxBytes+1))
	closeErr := temp.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tempPath)
		if copyErr != nil {
			return nil, copyErr
		}
		return nil, closeErr
	}
	if written > promptGalleryCoverMaxBytes {
		_ = os.Remove(tempPath)
		return nil, errors.New("封面超过大小上限")
	}
	if err := os.Rename(tempPath, cachePath); err != nil {
		_ = os.Remove(tempPath)
		return nil, err
	}

	file, err := os.Open(cachePath)
	if err != nil {
		return nil, err
	}
	return &PromptGalleryCover{Path: cachePath, MimeType: promptGalleryCoverMime(cachePath), Size: written, Body: file}, nil
}

/**
 * 校验并归一化封面相对路径。
 * 只接受来源自己的目录前缀，挡掉 `..`、绝对路径、查询串和反斜杠。
 */
func promptGalleryCoverRelativePath(descriptor promptGallerySourceDescriptor, coverPath string) (string, error) {
	raw := strings.TrimSpace(coverPath)
	if raw == "" {
		return "", BadAuthRequest("封面路径为空")
	}
	if strings.ContainsAny(raw, "\\?#") || strings.HasPrefix(raw, "/") {
		return "", BadAuthRequest("封面路径无效")
	}
	clean := filepath.ToSlash(filepath.Clean(raw))
	if clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", BadAuthRequest("封面路径无效")
	}
	if descriptor.CoverPrefix != "" && !strings.HasPrefix(clean, descriptor.CoverPrefix) {
		return "", BadAuthRequest("封面路径不属于该来源")
	}
	return clean, nil
}

func promptGalleryCoverCacheName(cleanPath string) string {
	sum := sha256.Sum256([]byte(cleanPath))
	ext := strings.ToLower(filepath.Ext(cleanPath))
	if len(ext) > 8 || strings.ContainsAny(ext, "/\\") {
		ext = ""
	}
	return hex.EncodeToString(sum[:]) + ext
}

func promptGalleryCoverMime(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}