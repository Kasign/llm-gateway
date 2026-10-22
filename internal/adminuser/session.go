package adminuser

import (
	"crypto/rand"
	"encoding/hex"
)

const SessionPrefix = "gw_sess_"

// NewSessionToken 生成带前缀的随机会话 token（与 sk-llm- API Key 区分）。
func NewSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return SessionPrefix + hex.EncodeToString(b), nil
}
