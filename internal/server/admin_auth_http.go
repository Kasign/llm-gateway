package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/adminuser"
	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/auth"
)

func (s *Server) sessionTTL() time.Duration {
	h := s.cfg.Auth.SessionTTLHours
	if h <= 0 {
		h = 24 * 7
	}
	return time.Duration(h) * time.Hour
}

func (s *Server) adminSessionToken(r *http.Request) (string, bool) {
	raw, ok := auth.ParseBearer(r.Header.Get("Authorization"))
	if !ok || !strings.HasPrefix(raw, adminuser.SessionPrefix) {
		return "", false
	}
	return raw, true
}

func (s *Server) adminSessionFromRequest(ctx context.Context, r *http.Request) (username string, token string, ok bool) {
	tok, ok := s.adminSessionToken(r)
	if !ok {
		return "", "", false
	}
	name, rev, sessOK, err := s.limiter.GetSession(ctx, tok)
	if err != nil || !sessOK || !s.sessionMatchesWebLogin(name, rev) {
		return "", "", false
	}
	return name, tok, true
}

func (s *Server) adminUsernameFromRequest(ctx context.Context, r *http.Request) string {
	name, _, ok := s.adminSessionFromRequest(ctx, r)
	if !ok {
		return ""
	}
	return name
}

// adminAuthorized 保护 /api/* 管理接口：Web 会话或 X-Admin-Key（自动化脚本）。
// 客户端调用 /v1/* 走 authenticate，仅校验 sk-llm- API Key，与会话无关。
func (s *Server) adminAuthorized(ctx context.Context, r *http.Request) bool {
	if s.cfg.Auth.AdminAPIKey != "" {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Admin-Key")), []byte(s.cfg.Auth.AdminAPIKey)) == 1 {
			return true
		}
	}
	return s.adminUsernameFromRequest(ctx, r) != ""
}

func (s *Server) authStatusAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method_not_allowed", "GET required")
		return
	}
	snap := s.currentWebLoginSnapshot()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]interface{}{"password_enabled": snap.PasswordEnabled})
}

func (s *Server) authLoginAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}

	// 同一份快照提供开关、密码哈希和会话版本，避免并发修改时混用新旧状态。
	snap := s.currentWebLoginSnapshot()
	cfgUser := strings.TrimSpace(snap.Username)
	if cfgUser == "" {
		jsonError(w, 503, "login_not_configured", "控制台登录状态未初始化")
		return
	}

	ip := clientIP(r, s.trustedProxyRanges)
	allowed, _, err := s.limiter.Allow(r.Context(), "login:"+ip, 12)
	if err != nil {
		jsonError(w, 503, "rate_limit_unavailable", err.Error())
		return
	}
	if !allowed {
		applog.Printf("admin login rate limited: ip=%s", ip)
		jsonError(w, 429, "too_many_attempts", "too many login attempts, try again later")
		return
	}

	var in struct {
		Password string `json:"password"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			jsonError(w, 400, "invalid_request", "invalid JSON body")
			return
		}
	}

	if snap.PasswordEnabled {
		if !adminuser.WebLoginConfigured(cfgUser, snap.PasswordHash) {
			jsonError(w, 503, "login_not_configured", "密码保护已启用，但未配置有效密码")
			return
		}
		if !adminuser.CheckPassword(snap.PasswordHash, in.Password) {
			applog.Printf("admin login failed: password mismatch from=%s", ip)
			jsonError(w, 401, "invalid_credentials", "密码错误")
			return
		}
	}

	token, err := adminuser.NewSessionToken()
	if err != nil {
		jsonError(w, 500, "session_error", err.Error())
		return
	}
	if err := s.limiter.SetSession(r.Context(), token, cfgUser, snap.Revision, s.sessionTTL()); err != nil {
		jsonError(w, 503, "session_unavailable", err.Error())
		return
	}

	applog.Printf("admin login ok: password_enabled=%t from=%s", snap.PasswordEnabled, ip)
	writeJSON(w, map[string]interface{}{
		"token": token,
		"user":  map[string]interface{}{"username": cfgUser},
	})
}

func (s *Server) authLogoutAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	if tok, ok := s.adminSessionToken(r); ok {
		_ = s.limiter.DeleteSession(r.Context(), tok)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) authMeAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method_not_allowed", "GET required")
		return
	}
	name := s.adminUsernameFromRequest(r.Context(), r)
	if name == "" {
		jsonError(w, 401, "unauthorized", "not logged in")
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"user": map[string]interface{}{"username": name},
	})
}

func (s *Server) authCredentialsAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.authCredentialsGet(w, r)
	case http.MethodPut:
		s.authCredentialsPut(w, r)
	default:
		jsonError(w, 405, "method_not_allowed", "GET or PUT required")
	}
}

func (s *Server) authCredentialsGet(w http.ResponseWriter, r *http.Request) {
    if !s.requireAdmin(w, r) {
        return
    }
    snap := s.currentWebLoginSnapshot()
    stored := false
    if rec, err := s.settings.GetWebLogin(r.Context()); err == nil && rec != nil {
        stored = true
    }
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "password_enabled": snap.PasswordEnabled,
        "stored_in_db": stored,
        "can_change_via": "web",
    })
}

func (s *Server) authCredentialsPut(w http.ResponseWriter, r *http.Request) {
	// 密码设置只能串行修改，避免并发请求基于旧版本覆盖对方的新值。
	s.webLoginMutationMu.Lock()
	defer s.webLoginMutationMu.Unlock()

	_, token, ok := s.adminSessionFromRequest(r.Context(), r)
	if !ok {
		jsonError(w, 401, "web_session_required", "修改密码设置需要网页登录会话")
		return
	}

	current := s.currentWebLoginSnapshot()
	cfgUser := strings.TrimSpace(current.Username)
	if cfgUser == "" {
		jsonError(w, 503, "login_not_configured", "控制台登录状态未初始化")
		return
	}

	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
		PasswordEnabled *bool  `json:"password_enabled"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		jsonError(w, 400, "invalid_request", "invalid JSON body")
		return
	}

	targetEnabled := current.PasswordEnabled
	if in.PasswordEnabled != nil {
		targetEnabled = *in.PasswordEnabled
	}
	if current.PasswordEnabled && !adminuser.CheckPassword(current.PasswordHash, in.CurrentPassword) {
		applog.Printf("password settings rejected: current password mismatch from=%s", clientIP(r, s.trustedProxyRanges))
		jsonError(w, 401, "invalid_credentials", "当前密码错误")
		return
	}
	if !current.PasswordEnabled && targetEnabled && strings.TrimSpace(in.NewPassword) == "" {
		jsonError(w, 400, "password_required", "开启密码保护时必须设置新密码")
		return
	}
	if !targetEnabled && strings.TrimSpace(in.NewPassword) != "" {
		jsonError(w, 400, "invalid_request", "关闭密码保护时请清空新密码输入框")
		return
	}

	newHash := current.PasswordHash
	if strings.TrimSpace(in.NewPassword) != "" {
		hash, err := adminuser.HashPassword(in.NewPassword)
		if err != nil {
			jsonError(w, 400, "invalid_password", err.Error())
			return
		}
		newHash = hash
	}
	if targetEnabled && !adminuser.WebLoginConfigured(cfgUser, newHash) {
		jsonError(w, 400, "password_required", "启用密码保护需要有效密码")
		return
	}
	if targetEnabled == current.PasswordEnabled && newHash == current.PasswordHash {
		jsonError(w, 400, "no_change", "没有需要保存的更改")
		return
	}

	nextRevision := current.Revision + 1
	next := webLoginSnapshot{
		Username:        webAdminIdentity,
		PasswordHash:    newHash,
		PasswordEnabled: targetEnabled,
		Revision:        nextRevision,
	}
	if err := s.persistWebLogin(r.Context(), next); err != nil {
		jsonError(w, 500, "save_failed", err.Error())
		return
	}
	if err := s.limiter.SetSession(r.Context(), token, webAdminIdentity, nextRevision, s.sessionTTL()); err != nil {
		jsonError(w, 503, "session_unavailable", err.Error())
		return
	}

	applog.Printf("web password settings updated: password_enabled=%t from=%s", targetEnabled, clientIP(r, s.trustedProxyRanges))
	writeJSON(w, map[string]interface{}{
		"user":             map[string]interface{}{"username": webAdminIdentity},
		"password_enabled": targetEnabled,
	})
}

