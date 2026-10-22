// auth 处理调用方 API Key：生成、哈希存储、从 Authorization 头解析，不负责 HTTP 层本身。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Key 表示一条已登记的 API 密钥元数据（库里只存哈希，不存明文）。
type Key struct {
	ID            uint64     `json:"id"`
	Name          string     `json:"name"`
	Prefix        string     `json:"prefix"`
	Hash          string     `json:"-"`
	Status        string     `json:"status"`
	RPM           int        `json:"rpm"`
	AllowedModels []string   `json:"allowed_models"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	CreatedAt     *time.Time `json:"created_at,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
}

// NewAPIKey 生成 sk-llm- 前缀的随机密钥，并返回明文、展示用前缀与 SHA256 哈希。
func NewAPIKey() (string, string, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", "", err
	}
	raw := "sk-llm-" + hex.EncodeToString(b)
	return raw, raw[:18], Hash(raw), nil
}

// Hash 把明文 API Key 变成不可逆摘要，数据库里只存这个值。
func Hash(raw string) string { s := sha256.Sum256([]byte(raw)); return hex.EncodeToString(s[:]) }

// ParseBearer 从 "Bearer xxx" 取出 token；格式不对返回 false。
func ParseBearer(v string) (string, bool) {
	v = strings.TrimSpace(v)
	i := strings.IndexByte(v, ' ')
	if i <= 0 {
		return "", false
	}
	if !strings.EqualFold(v[:i], "Bearer") {
		return "", false
	}
	k := strings.TrimSpace(v[i+1:])
	return k, k != ""
}

// ModelAllowed is reserved for future per-key model ACL; currently keys only gate access.
func ModelAllowed(_ Key, _ string) bool { return true }
