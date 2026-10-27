package server

import (
    "context"
    "strings"

    "github.com/whl/llm-gateway/internal/adminuser"
    "github.com/whl/llm-gateway/internal/applog"
    "github.com/whl/llm-gateway/internal/settings"
)

const webAdminIdentity = "admin"

type webLoginSnapshot struct {
    Username string
    PasswordHash string
    PasswordEnabled bool
    PasswordSource string
    Revision uint64
}

func (s *Server) loadWebLoginState(ctx context.Context) {
    hash := strings.TrimSpace(s.cfg.Auth.AdminPasswordHash)
    enabled := s.cfg.Auth.WebPasswordEnabled && adminuser.WebLoginConfigured(webAdminIdentity, hash)
    revision := uint64(0)
    source := "deployment"
    if s.cfg.Auth.WebLoginResetFromEnv {
        // 忘记密码时显式临时跳过数据库旧值；保存新密码后必须移除此环境开关。
        applog.Printf("web login: reset-from-env override active; stored database credentials are temporarily bypassed")
    } else if stored, err := s.settings.GetWebLogin(ctx); err == nil && stored != nil {
        storedHash := strings.TrimSpace(stored.PasswordHash)
        storedEnabled := false
        useStored := true
        if stored.PasswordEnabled == nil {
            // 旧记录没有开关字段：有效哈希代表原有密码登录继续启用。
            if !adminuser.WebLoginConfigured(webAdminIdentity, storedHash) {
                applog.Printf("web login: ignoring invalid legacy web_admin_login in database, using config defaults")
                useStored = false
            } else {
                storedEnabled = true
            }
        } else {
            storedEnabled = *stored.PasswordEnabled
            if storedEnabled && !adminuser.WebLoginConfigured(webAdminIdentity, storedHash) {
                applog.Printf("web login: ignoring enabled web_admin_login without valid password hash, using config defaults")
                useStored = false
            }
        }
        if useStored {
            hash, enabled, revision = storedHash, storedEnabled, stored.Revision
            source = "database"
        }
    }
    applog.Printf("web login state loaded: source=%s password_enabled=%t hash_format=%s", source, enabled, webLoginHashFormat(hash))
    s.webLoginMu.Lock()
    s.webLogin = webLoginSnapshot{
        Username: webAdminIdentity,
        PasswordHash: hash,
        PasswordEnabled: enabled,
        PasswordSource: source,
        Revision: revision,
    }
    s.webLoginMu.Unlock()
}

func webLoginHashFormat(hash string) string {
    switch {
    case strings.HasPrefix(strings.TrimSpace(hash), "pbkdf2:"):
        return "colon"
    case strings.HasPrefix(strings.TrimSpace(hash), "pbkdf2$"):
        return "legacy-dollar"
    default:
        return "missing-or-invalid"
    }
}

func (s *Server) currentWebLoginSnapshot() webLoginSnapshot {
    s.webLoginMu.RLock()
    defer s.webLoginMu.RUnlock()
    return s.webLogin
}

func (s *Server) effectiveWebLogin() (username, passwordHash string, revision uint64, ok bool) {
    snap := s.currentWebLoginSnapshot()
    if strings.TrimSpace(snap.Username) == "" {
        return "", "", 0, false
    }
    return snap.Username, snap.PasswordHash, snap.Revision, true
}

func (s *Server) sessionMatchesWebLogin(username string, revision uint64) bool {
    snap := s.currentWebLoginSnapshot()
    return strings.TrimSpace(snap.Username) != "" && username == snap.Username && revision == snap.Revision
}

func (s *Server) persistWebLogin(ctx context.Context, snap webLoginSnapshot) error {
    enabled := snap.PasswordEnabled
    if err := s.settings.SaveWebLogin(ctx, settings.WebLoginStored{
        Username: webAdminIdentity,
        PasswordHash: snap.PasswordHash,
        Revision: snap.Revision,
        PasswordEnabled: &enabled,
    }); err != nil {
        return err
    }
    snap.PasswordSource = "database"
    s.webLoginMu.Lock()
    s.webLogin = snap
    s.webLoginMu.Unlock()
    return nil
}
