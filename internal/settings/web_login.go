package settings

import (
    "context"
    "strings"
)

const WebLoginKey = "web_admin_login"

// WebLoginStored 保存控制台密码保护策略。旧记录没有开关字段时按有效哈希推断为开启。
type WebLoginStored struct {
    Username string `json:"username"`
    PasswordHash string `json:"password_hash"`
    Revision uint64 `json:"revision"`
    PasswordEnabled *bool `json:"password_enabled,omitempty"`
}

func (r *Repo) GetWebLogin(ctx context.Context) (*WebLoginStored, error) {
    var s WebLoginStored
    ok, err := r.GetJSON(ctx, WebLoginKey, &s)
    if err != nil || !ok {
        return nil, err
    }
    s.Username = strings.TrimSpace(s.Username)
    if s.Username == "" {
        s.Username = "admin"
    }
    s.PasswordHash = strings.TrimSpace(s.PasswordHash)
    if s.PasswordEnabled == nil && s.PasswordHash == "" {
        return nil, nil
    }
    return &s, nil
}

func (r *Repo) SaveWebLogin(ctx context.Context, s WebLoginStored) error {
    s.Username = "admin"
    s.PasswordHash = strings.TrimSpace(s.PasswordHash)
    return r.SetJSON(ctx, WebLoginKey, s)
}
