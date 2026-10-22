package settings

import (
	"context"
	"strings"
)

const WebLoginKey = "web_admin_login"

// WebLoginStored 控制台 Web 登录凭据（覆盖 config 中的初始值，密码仅存哈希）。
type WebLoginStored struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	Revision     uint64 `json:"revision"`
}

func (r *Repo) GetWebLogin(ctx context.Context) (*WebLoginStored, error) {
	var s WebLoginStored
	ok, err := r.GetJSON(ctx, WebLoginKey, &s)
	if err != nil || !ok {
		return nil, err
	}
	s.Username = strings.TrimSpace(s.Username)
	s.PasswordHash = strings.TrimSpace(s.PasswordHash)
	if s.Username == "" || s.PasswordHash == "" {
		return nil, nil
	}
	return &s, nil
}

func (r *Repo) SaveWebLogin(ctx context.Context, s WebLoginStored) error {
	return r.SetJSON(ctx, WebLoginKey, s)
}
