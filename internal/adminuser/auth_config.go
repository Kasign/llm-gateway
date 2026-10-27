package adminuser

import (
	"fmt"
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

// WebLoginConfigured 是否已配置 Web 控制台登录。
func WebLoginConfigured(username, passwordHash string) bool {
	passwordHash = strings.TrimSpace(passwordHash)
	return strings.TrimSpace(username) != "" && passwordHash != "" &&
		(strings.HasPrefix(passwordHash, "pbkdf2$") || strings.HasPrefix(passwordHash, "pbkdf2:"))
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
