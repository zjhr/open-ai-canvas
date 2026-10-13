package model

import "time"

// PromptGallerySource 记录提示词画廊某个远端来源的同步状态。
// 只保存元信息：正文都落在 PromptGalleryPrompt，来源表负责"是否已同步/哪个版本"。
type PromptGallerySource struct {
	ID           string     `gorm:"primaryKey;size:64"`
	Name         string     `gorm:"size:120;not null"`
	License      string     `gorm:"size:120"`
	Revision     string     `gorm:"size:96"`
	TotalRows    int64      `gorm:"not null;default:0"`
	LastSyncedAt *time.Time `gorm:"index"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (PromptGallerySource) TableName() string { return "prompt_gallery_sources" }

// PromptGalleryPrompt 是提示词画廊的服务端索引行。
//
// 只存检索与展示需要的文本和元数据，不落地上游封面文件（封面对应关系存 CoverPath，
// 后续按需经代理读取并缓存）。Tags 用空格拼接，便于普通 LIKE 检索，两种数据库行为一致。
type PromptGalleryPrompt struct {
	ID         string `gorm:"primaryKey;size:160"`
	// 组合唯一索引同时承担按来源过滤（最左前缀）与同步幂等写入。
	SourceID   string `gorm:"size:64;not null;uniqueIndex:idx_prompt_gallery_source_external,priority:1"`
	ExternalID string `gorm:"size:96;not null;uniqueIndex:idx_prompt_gallery_source_external,priority:2"`
	Mode       string `gorm:"size:16;not null;index"`
	Title          string    `gorm:"size:200;not null"`
	Description    string    `gorm:"type:text"`
	Prompt         string    `gorm:"type:text"`
	PromptZh       string    `gorm:"type:text"`
	Category       string    `gorm:"size:96;index"`
	Model          string    `gorm:"size:120"`
	Tags           string    `gorm:"size:500"`
	Credit         string    `gorm:"size:160"`
	SourceURL      string    `gorm:"size:500"`
	CoverPath      string    `gorm:"size:300"`
	Ratio          string    `gorm:"size:32"`
	Featured       bool      `gorm:"index"`
	SourceRevision string    `gorm:"size:96"`
	SyncedAt       time.Time `gorm:"index"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (PromptGalleryPrompt) TableName() string { return "prompt_gallery_prompts" }