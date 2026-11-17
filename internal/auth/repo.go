// auth（repo）：API Key 的增删改查与轮换，数据落在 api_keys 表。
package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Repo 通过 SQL 读写密钥记录。
type Repo struct{ db *sql.DB }

// NewRepo 绑定已打开的数据库连接。
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

// decodeAllowedTools 将旧记录的 NULL/空值和无效 JSON 安全降级为空授权，避免旧 Key 获得工具权限。
func decodeAllowedTools(raw string) []string {
	var names []string
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &names)
	}
	if names == nil {
		return []string{}
	}
	return names
}

// Create 新建密钥；返回结构体与仅此次可见的完整明文，请妥善保存。
func (r *Repo) Create(ctx context.Context, name string, rpm int, allowed []string, expires *time.Time) (Key, string, error) {
	return r.CreateWithTools(ctx, name, rpm, allowed, nil, expires)
}

// CreateWithTools 在现有调用方 Key 上保存独立的工具授权列表。
func (r *Repo) CreateWithTools(ctx context.Context, name string, rpm int, allowed, allowedTools []string, expires *time.Time) (Key, string, error) {
	raw, prefix, h, err := NewAPIKey()
	if err != nil {
		return Key{}, "", err
	}
	if rpm <= 0 {
		rpm = 60
	}
	data, _ := json.Marshal(allowed)
	toolData, _ := json.Marshal(allowedTools)
	now := time.Now()
	res, err := r.db.ExecContext(ctx, `INSERT INTO api_keys(name,prefix,key_hash,status,expires_at,rpm,allowed_models,allowed_tools,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, name, prefix, h, "active", expires, rpm, string(data), string(toolData), now, now)
	if err != nil {
		return Key{}, "", err
	}
	id, _ := res.LastInsertId()
	return Key{ID: uint64(id), Name: name, Prefix: prefix, Hash: h, Status: "active", RPM: rpm, AllowedModels: allowed, AllowedTools: append([]string{}, allowedTools...), ExpiresAt: expires, CreatedAt: &now, UpdatedAt: &now}, raw, nil
}

// GetByRaw 用请求里带来的明文 key 查库（内部会先哈希再比对）。
func (r *Repo) GetByRaw(ctx context.Context, raw string) (*Key, error) {
	h := Hash(raw)
	row := r.db.QueryRowContext(ctx, `SELECT id,name,prefix,key_hash,status,rpm,COALESCE(allowed_models,''),COALESCE(allowed_tools,''),expires_at,created_at,updated_at,last_used_at FROM api_keys WHERE key_hash=?`, h)
	var k Key
	var models, toolsJSON string
	if err := row.Scan(&k.ID, &k.Name, &k.Prefix, &k.Hash, &k.Status, &k.RPM, &models, &toolsJSON, &k.ExpiresAt, &k.CreatedAt, &k.UpdatedAt, &k.LastUsedAt); err != nil {
		return nil, err
	}
	if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
		k.Status = "expired"
	}
	if models != "" {
		_ = json.Unmarshal([]byte(models), &k.AllowedModels)
	}
	k.AllowedTools = decodeAllowedTools(toolsJSON)
	return &k, nil
}

func (r *Repo) Touch(ctx context.Context, id uint64) {
	_, _ = r.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at=?,updated_at=? WHERE id=?`, time.Now(), time.Now(), id)
}

func (r *Repo) List(ctx context.Context) ([]Key, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,name,prefix,status,rpm,COALESCE(allowed_models,''),COALESCE(allowed_tools,''),expires_at,created_at,updated_at,last_used_at FROM api_keys ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		var k Key
		var m, toolJSON string
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.Status, &k.RPM, &m, &toolJSON, &k.ExpiresAt, &k.CreatedAt, &k.UpdatedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		if m != "" { _ = json.Unmarshal([]byte(m), &k.AllowedModels) }
		k.AllowedTools = decodeAllowedTools(toolJSON)
		if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
			k.Status = "expired"
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (r *Repo) Update(ctx context.Context, id uint64, name string, rpm int, allowed []string, expires *time.Time) error {
	return r.UpdateWithTools(ctx, id, name, rpm, allowed, nil, expires)
}

// UpdateWithTools 在 allowedTools 为 nil 时保留原工具权限；非 nil 空切片表示显式清空。
func (r *Repo) UpdateWithTools(ctx context.Context, id uint64, name string, rpm int, allowed, allowedTools []string, expires *time.Time) error {
	if rpm <= 0 {
		rpm = 60
	}
	data, _ := json.Marshal(allowed)
	var toolData any
	if allowedTools != nil { encoded, _ := json.Marshal(allowedTools); toolData = string(encoded) }
	res, err := r.db.ExecContext(ctx, `UPDATE api_keys SET name=?,rpm=?,allowed_models=?,allowed_tools=COALESCE(?,allowed_tools),expires_at=?,updated_at=? WHERE id=?`, name, rpm, string(data), toolData, expires, time.Now(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("api key not found")
	}
	return nil
}

func (r *Repo) Rotate(ctx context.Context, id uint64) (string, error) {
	raw, prefix, h, err := NewAPIKey()
	if err != nil {
		return "", err
	}
	res, err := r.db.ExecContext(ctx, `UPDATE api_keys SET prefix=?,key_hash=?,status='active',last_used_at=NULL,updated_at=? WHERE id=?`, prefix, h, time.Now(), id)
	if err != nil {
		return "", err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return "", fmt.Errorf("api key not found")
	}
	return raw, nil
}
func (r *Repo) SetStatus(ctx context.Context, id uint64, status string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE api_keys SET status=?,updated_at=? WHERE id=?`, status, time.Now(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("api key not found")
	}
	return nil
}
func (r *Repo) Delete(ctx context.Context, id uint64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("api key not found")
	}
	return nil
}
