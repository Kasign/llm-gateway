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

// Create 新建密钥；返回结构体与仅此次可见的完整明文，请妥善保存。
func (r *Repo) Create(ctx context.Context, name string, rpm int, allowed []string, expires *time.Time) (Key, string, error) {
	raw, prefix, h, err := NewAPIKey()
	if err != nil {
		return Key{}, "", err
	}
	if rpm <= 0 {
		rpm = 60
	}
	data, _ := json.Marshal(allowed)
	now := time.Now()
	res, err := r.db.ExecContext(ctx, `INSERT INTO api_keys(name,prefix,key_hash,status,expires_at,rpm,allowed_models,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, name, prefix, h, "active", expires, rpm, string(data), now, now)
	if err != nil {
		return Key{}, "", err
	}
	id, _ := res.LastInsertId()
	return Key{ID: uint64(id), Name: name, Prefix: prefix, Hash: h, Status: "active", RPM: rpm, AllowedModels: allowed, ExpiresAt: expires, CreatedAt: &now, UpdatedAt: &now}, raw, nil
}

// GetByRaw 用请求里带来的明文 key 查库（内部会先哈希再比对）。
func (r *Repo) GetByRaw(ctx context.Context, raw string) (*Key, error) {
	h := Hash(raw)
	row := r.db.QueryRowContext(ctx, `SELECT id,name,prefix,key_hash,status,rpm,COALESCE(allowed_models,''),expires_at,created_at,updated_at,last_used_at FROM api_keys WHERE key_hash=?`, h)
	var k Key
	var models string
	if err := row.Scan(&k.ID, &k.Name, &k.Prefix, &k.Hash, &k.Status, &k.RPM, &models, &k.ExpiresAt, &k.CreatedAt, &k.UpdatedAt, &k.LastUsedAt); err != nil {
		return nil, err
	}
	if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
		k.Status = "expired"
	}
	if models != "" {
		_ = json.Unmarshal([]byte(models), &k.AllowedModels)
	}
	return &k, nil
}

func (r *Repo) Touch(ctx context.Context, id uint64) {
	_, _ = r.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at=?,updated_at=? WHERE id=?`, time.Now(), time.Now(), id)
}

func (r *Repo) List(ctx context.Context) ([]Key, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,name,prefix,status,rpm,COALESCE(allowed_models,''),expires_at,created_at,updated_at,last_used_at FROM api_keys ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Key
	for rows.Next() {
		var k Key
		var m string
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.Status, &k.RPM, &m, &k.ExpiresAt, &k.CreatedAt, &k.UpdatedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		if m != "" {
			_ = json.Unmarshal([]byte(m), &k.AllowedModels)
		}
		if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
			k.Status = "expired"
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (r *Repo) Update(ctx context.Context, id uint64, name string, rpm int, allowed []string, expires *time.Time) error {
	if rpm <= 0 {
		rpm = 60
	}
	data, _ := json.Marshal(allowed)
	res, err := r.db.ExecContext(ctx, `UPDATE api_keys SET name=?,rpm=?,allowed_models=?,expires_at=?,updated_at=? WHERE id=?`, name, rpm, string(data), expires, time.Now(), id)
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
	res, err := r.db.ExecContext(ctx, `UPDATE api_keys SET prefix=?,key_hash=?,status='active',updated_at=? WHERE id=?`, prefix, h, time.Now(), id)
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
	_, err := r.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id=?`, id)
	return err
}
