// model 定义「网关管理的模型」在内存/JSON 中的形状（本地 GGUF、HF 来源、加载状态等）。
package model

import "time"

// Model 对应 models 表一行：业务上的 model_key 与磁盘路径、上游注册名分开存。
type Model struct {
	ID             uint64     `json:"id"`
	ModelKey       string     `json:"model_key"`
	DisplayName    string     `json:"display_name"`
	SourceType     string     `json:"source_type"`
	HFRepo         string     `json:"hf_repo,omitempty"`
	HFRevision     string     `json:"hf_revision,omitempty"`
	Filename       string     `json:"filename"`
	LocalPath      string     `json:"local_path"`
	Quantization   string     `json:"quantization,omitempty"`
	LlamaModelName string     `json:"llama_model_name,omitempty"`
	SizeBytes      int64      `json:"size_bytes,omitempty"`
	SHA256         string     `json:"sha256,omitempty"`
	Status         string     `json:"status"`
	Enabled        bool       `json:"enabled"`
	RuntimeJSON    string     `json:"runtime_json,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastLoadedAt   *time.Time `json:"last_loaded_at,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
}
