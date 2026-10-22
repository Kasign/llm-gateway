package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// GetJSON loads a gateway_settings row into dest. Returns false when missing.
func (r *Repo) GetJSON(ctx context.Context, key string, dest interface{}) (bool, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT value_json FROM gateway_settings WHERE key_name = ?`, key).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), dest); err != nil {
		return false, fmt.Errorf("parse settings %s: %w", key, err)
	}
	return true, nil
}

// SetJSON upserts a JSON value for key_name.
func (r *Repo) SetJSON(ctx context.Context, key string, value interface{}) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = r.db.ExecContext(ctx, `
INSERT INTO gateway_settings (key_name, value_json, updated_at)
VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE value_json = VALUES(value_json), updated_at = VALUES(updated_at)`,
		key, string(b), now)
	return err
}
