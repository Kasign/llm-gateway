// catalog 提供模型发现（HF / Ollama 库）的统一数据结构，与下载、加载解耦。
package catalog

import "time"

// Source identifies where a listing comes from.
type Source string

const (
	SourceHuggingFace Source = "huggingface"
	SourceOllama      Source = "ollama"
)

// SortKey is supported server-side sort mode.
type SortKey string

const (
	SortHot    SortKey = "hot"    // downloads / pulls descending
	SortNewest SortKey = "newest" // last modified / updated descending
)

// Query options for Search.
type Query struct {
	Text       string
	Source     string // all | hf | huggingface | ollama
	Sort       SortKey
	Limit      int
	HFGGUFOnly bool // when querying HF, restrict to GGUF repos
}

// Listing is one discoverable model row for the admin UI.
type Listing struct {
	Source        Source     `json:"source"`
	ID            string     `json:"id"`
	DisplayName   string     `json:"display_name"`
	CopyName      string     `json:"copy_name"`
	Description   string     `json:"description,omitempty"`
	Downloads     *int64     `json:"downloads,omitempty"`
	Likes         *int64     `json:"likes,omitempty"`
	PublishedAt   *time.Time `json:"published_at,omitempty"`
	UpdatedLabel  string     `json:"updated_label,omitempty"`
	Tags          []string   `json:"tags,omitempty"`
	ParamTags     []string   `json:"param_tags,omitempty"` // Ollama library variant labels (8b, 3b), not disk size
	PipelineTag   string     `json:"pipeline_tag,omitempty"`
	SortDownloads int64      `json:"-"`
	SortTime      time.Time  `json:"-"`
}
