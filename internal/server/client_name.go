package server

import (
	"net/http"
	"strings"
)

// requestClientName 写入调用记录的「客户端」。
// 优先用 X-Client-Name。Codex 等调用方不带这个头，就从 User-Agent 取一个短名称，避免列表里一直是空的。
func requestClientName(r *http.Request) string {
	if r == nil {
		return ""
	}
	return displayClientName(r.Header.Get("X-Client-Name"), r.UserAgent())
}

func displayClientName(name, userAgent string) string {
	name = trimClientName(name)
	if name != "" {
		return name
	}
	return clientNameFromUserAgent(userAgent)
}

func clientNameFromUserAgent(ua string) string {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return ""
	}
	if strings.Contains(strings.ToLower(ua), "codex") {
		return "Codex"
	}
	field := ua
	if i := strings.IndexAny(field, " \t"); i > 0 {
		field = field[:i]
	}
	if i := strings.IndexByte(field, '/'); i > 0 {
		field = field[:i]
	}
	// 浏览器 UA 都以 Mozilla 开头，拿它当客户端名没有意义。
	if strings.EqualFold(field, "Mozilla") {
		return ""
	}
	return trimClientName(field)
}

func trimClientName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	r := []rune(s)
	if len(r) > 128 {
		return string(r[:128])
	}
	return s
}
