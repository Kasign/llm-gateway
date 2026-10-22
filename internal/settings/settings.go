// settings 保存可在管理界面修改、并写入数据库的运行时配置（例如当前 LLM 上游地址）。
// 覆盖 deploy 里的静态配置，便于不重启就切换后端。
package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const llmUpstreamKey = "llm_upstream"

// Upstream 描述当前生效的推理后端（llamacpp 或 ollama）及其 URL、默认模型、模型目录。
// ModelsByBackend 记住每个后端上次用于 /v1 的模型，切换上游时按后端恢复，避免把 llama.cpp 的名字带到 Ollama。
// Upstream is the runtime LLM backend configuration (overrides deploy config when saved via admin UI).
type Upstream struct {
	Backend         string            `json:"backend"`
	BaseURL         string            `json:"base_url"`
	ModelName       string            `json:"model_name,omitempty"`
	ModelsDir       string            `json:"models_dir,omitempty"`
	ModelsByBackend map[string]string `json:"models_by_backend,omitempty"`
}

func (u Upstream) Validate() error {
	u.Backend = strings.ToLower(strings.TrimSpace(u.Backend))
	if u.Backend != "llamacpp" && u.Backend != "ollama" {
		return fmt.Errorf("backend must be llamacpp or ollama")
	}
	u.BaseURL = strings.TrimSpace(u.BaseURL)
	if u.BaseURL == "" {
		return fmt.Errorf("base_url is required")
	}
	if strings.Contains(u.BaseURL, "${") {
		return fmt.Errorf("base_url is unresolved")
	}
	return nil
}

func Normalize(u Upstream) Upstream {
	u.Backend = strings.ToLower(strings.TrimSpace(u.Backend))
	u.BaseURL = strings.TrimRight(strings.TrimSpace(u.BaseURL), "/")
	u.ModelName = strings.TrimSpace(u.ModelName)
	u.ModelsDir = strings.TrimSpace(u.ModelsDir)
	u.ModelsByBackend = normalizeBackendModels(u.ModelsByBackend)
	return u
}

func normalizeBackendModels(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if (k != "llamacpp" && k != "ollama") || v == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Repo 读写 gateway_settings 表中的 JSON 配置项。
type Repo struct {
	db *sql.DB
}

func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db}
}

func (r *Repo) GetUpstream(ctx context.Context) (*Upstream, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT value_json FROM gateway_settings WHERE key_name = ?`, llmUpstreamKey).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var u Upstream
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		return nil, fmt.Errorf("parse llm_upstream: %w", err)
	}
	u = Normalize(u)
	return &u, nil
}

func (r *Repo) SaveUpstream(ctx context.Context, u Upstream) error {
	u = Normalize(u)
	if err := u.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(u)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = r.db.ExecContext(ctx, `
INSERT INTO gateway_settings (key_name, value_json, updated_at)
VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE value_json = VALUES(value_json), updated_at = VALUES(updated_at)`,
		llmUpstreamKey, string(b), now)
	return err
}
