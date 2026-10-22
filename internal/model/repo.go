// model（repo）：models 表的 CRUD 与状态标记（下载中、已加载、最近使用等）。
package model

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Repo 负责模型元数据持久化。
type Repo struct{ db *sql.DB }

func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

// Upsert 按 model_key 插入或更新，安装/同步模型目录时常用。
func (r *Repo) Upsert(ctx context.Context, m Model) error {
	now := time.Now()
	_, err := r.db.ExecContext(ctx, `
INSERT INTO models(model_key,display_name,source_type,hf_repo,hf_revision,filename,local_path,quantization,llama_model_name,size_bytes,sha256,status,enabled,runtime_json,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON DUPLICATE KEY UPDATE display_name=VALUES(display_name),source_type=VALUES(source_type),hf_repo=VALUES(hf_repo),hf_revision=VALUES(hf_revision),filename=VALUES(filename),local_path=VALUES(local_path),quantization=VALUES(quantization),llama_model_name=VALUES(llama_model_name),size_bytes=VALUES(size_bytes),sha256=VALUES(sha256),status=VALUES(status),enabled=VALUES(enabled),runtime_json=VALUES(runtime_json),updated_at=VALUES(updated_at)`,
		m.ModelKey, m.DisplayName, m.SourceType, m.HFRepo, m.HFRevision, m.Filename, m.LocalPath, m.Quantization, m.LlamaModelName, m.SizeBytes, m.SHA256, m.Status, m.Enabled, m.RuntimeJSON, now, now)
	return err
}

func (r *Repo) GetByKey(ctx context.Context, key string) (*Model, error) {
	row := r.db.QueryRowContext(ctx, `SELECT id,model_key,display_name,source_type,hf_repo,hf_revision,filename,local_path,quantization,COALESCE(llama_model_name,''),COALESCE(size_bytes,0),COALESCE(sha256,''),status,enabled,COALESCE(runtime_json,''),created_at,updated_at,last_loaded_at,last_used_at FROM models WHERE model_key=?`, key)
	var m Model
	if err := row.Scan(&m.ID, &m.ModelKey, &m.DisplayName, &m.SourceType, &m.HFRepo, &m.HFRevision, &m.Filename, &m.LocalPath, &m.Quantization, &m.LlamaModelName, &m.SizeBytes, &m.SHA256, &m.Status, &m.Enabled, &m.RuntimeJSON, &m.CreatedAt, &m.UpdatedAt, &m.LastLoadedAt, &m.LastUsedAt); err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *Repo) List(ctx context.Context) ([]Model, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,model_key,display_name,source_type,hf_repo,hf_revision,filename,local_path,quantization,COALESCE(llama_model_name,''),COALESCE(size_bytes,0),COALESCE(sha256,''),status,enabled,COALESCE(runtime_json,''),created_at,updated_at,last_loaded_at,last_used_at FROM models ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Model
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.ID, &m.ModelKey, &m.DisplayName, &m.SourceType, &m.HFRepo, &m.HFRevision, &m.Filename, &m.LocalPath, &m.Quantization, &m.LlamaModelName, &m.SizeBytes, &m.SHA256, &m.Status, &m.Enabled, &m.RuntimeJSON, &m.CreatedAt, &m.UpdatedAt, &m.LastLoadedAt, &m.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *Repo) Delete(ctx context.Context, key string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM models WHERE model_key=?`, key)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("model not found")
	}
	return nil
}

func (r *Repo) SetStatus(ctx context.Context, key, status string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE models SET status=?,updated_at=? WHERE model_key=?`, status, time.Now(), key)
	return err
}
func (r *Repo) MarkLoaded(ctx context.Context, key string) error {
	now := time.Now()
	_, err := r.db.ExecContext(ctx, `UPDATE models SET status='loaded',last_loaded_at=?,updated_at=? WHERE model_key=?`, now, now, key)
	return err
}
func (r *Repo) MarkUsed(ctx context.Context, key string) error {
	now := time.Now()
	_, err := r.db.ExecContext(ctx, `UPDATE models SET last_used_at=?,updated_at=? WHERE model_key=?`, now, now, key)
	return err
}

func (r *Repo) SetHash(ctx context.Context, key, sha string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE models SET sha256=?,updated_at=? WHERE model_key=?`, sha, time.Now(), key)
	return err
}

func (r *Repo) SetArtifact(ctx context.Context, key string, sizeBytes int64, sha string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE models SET size_bytes=?,sha256=?,updated_at=? WHERE model_key=?`, sizeBytes, sha, time.Now(), key)
	return err
}

func (r *Repo) SetSizeBytes(ctx context.Context, key string, sizeBytes int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE models SET size_bytes=?,updated_at=? WHERE model_key=?`, sizeBytes, time.Now(), key)
	return err
}

func (r *Repo) SetRuntimeJSON(ctx context.Context, key, runtimeJSON string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE models SET runtime_json=?,updated_at=? WHERE model_key=?`, runtimeJSON, time.Now(), key)
	return err
}
