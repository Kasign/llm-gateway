package adminuser

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// NormalizeWebLogin 将环境变量中的明文密码转为内存中的哈希，并清掉明文字段。
func NormalizeWebLogin(username, plain, hash *string) error {
	if username != nil {
		*username = strings.TrimSpace(*username)
	}
	if hash == nil {
		return nil
	}
	h := strings.TrimSpace(*hash)
	p := ""
	if plain != nil {
		p = *plain
		if strings.Contains(p, "${") {
			p = ""
		}
	}
	if h == "" && p != "" {
		var err error
		h, err = HashPassword(p)
		if err != nil {
			return err
		}
	}
	if plain != nil {
		*plain = ""
	}
	*hash = h
	return nil
}

// WebLoginConfigured 检查 Web 控制台用户名与密码哈希是否具备可校验的结构。
func WebLoginConfigured(username, passwordHash string) bool {
	if strings.TrimSpace(username) == "" {
		return false
	}
	passwordHash = strings.TrimSpace(passwordHash)
	var parts []string
	switch {
	case strings.HasPrefix(passwordHash, "pbkdf2$"):
		parts = strings.Split(passwordHash, "$")
	case strings.HasPrefix(passwordHash, "pbkdf2:"):
		parts = strings.Split(passwordHash, ":")
	default:
		return false
	}
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) == 0 {
		return false
	}
	digest, err := base64.RawStdEncoding.DecodeString(parts[3])
	return err == nil && len(digest) == 32
}

// ValidUsername 校验控制台用户名（3–64，字母数字与下划线）。
func ValidUsername(name string) error {
	name = strings.TrimSpace(name)
	if len(name) < 3 || len(name) > 64 {
		return fmt.Errorf("username must be 3–64 characters")
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return fmt.Errorf("username may only contain letters, digits, and underscore")
	}
	return nil
}
