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

func (s *Server) authLoginAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	cfgUser, cfgHash, rev, ok := s.effectiveWebLogin()
	if !ok {
		jsonError(w, 503, "login_not_configured", "未配置控制台登录，请设置管理员用户名与密码哈希")
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
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		jsonError(w, 400, "invalid_request", "invalid JSON body")
		return
	}
	user := strings.TrimSpace(in.Username)
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(cfgUser)) == 1
	passOK := adminuser.CheckPassword(cfgHash, in.Password)
	if !userOK || !passOK {
		applog.Printf("admin login failed: username=%q from=%s", user, ip)
		jsonError(w, 401, "invalid_credentials", "用户名或密码错误")
		return
	}
	token, err := adminuser.NewSessionToken()
	if err != nil {
		jsonError(w, 500, "session_error", err.Error())
		return
	}
	if err := s.limiter.SetSession(r.Context(), token, cfgUser, rev, s.sessionTTL()); err != nil {
		jsonError(w, 503, "session_unavailable", err.Error())
		return
	}
	applog.Printf("admin login ok: username=%q from=%s", cfgUser, ip)
	json.NewEncoder(w).Encode(map[string]interface{}{
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
	user, _, _, ok := s.effectiveWebLogin()
	if !ok {
		jsonError(w, 503, "login_not_configured", "未配置控制台登录，请设置管理员用户名与密码哈希")
		return
	}
	stored := false
	if rec, err := s.settings.GetWebLogin(r.Context()); err == nil && rec != nil {
		stored = true
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"username":       user,
		"stored_in_db":   stored,
		"can_change_via": "web",
	})
}

func (s *Server) authCredentialsPut(w http.ResponseWriter, r *http.Request) {
	_, tok, ok := s.adminSessionFromRequest(r.Context(), r)
	if !ok {
		jsonError(w, 401, "web_session_required", "change credentials requires web login session")
		return
	}
	curUser, curHash, curRev, ok := s.effectiveWebLogin()
	if !ok {
		jsonError(w, 503, "login_not_configured", "未配置控制台登录，请设置管理员用户名与密码哈希")
		return
	}
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewUsername     string `json:"new_username"`
		NewPassword     string `json:"new_password"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		jsonError(w, 400, "invalid_request", "invalid JSON body")
		return
	}
	if !adminuser.CheckPassword(curHash, in.CurrentPassword) {
		applog.Printf("credentials change rejected: bad current password from=%s", clientIP(r, s.trustedProxyRanges))
		jsonError(w, 401, "invalid_credentials", "current password is incorrect")
		return
	}
	newUser := curUser
	if u := strings.TrimSpace(in.NewUsername); u != "" {
		if err := adminuser.ValidUsername(u); err != nil {
			jsonError(w, 400, "invalid_username", err.Error())
			return
		}
		newUser = u
	}
	newHash := curHash
	if in.NewPassword != "" {
		h, err := adminuser.HashPassword(in.NewPassword)
		if err != nil {
			jsonError(w, 400, "invalid_password", err.Error())
			return
		}
		newHash = h
	}
	if newUser == curUser && newHash == curHash {
		jsonError(w, 400, "no_change", "nothing to update")
		return
	}
	nextRev := curRev + 1
	snap := webLoginSnapshot{Username: newUser, PasswordHash: newHash, Revision: nextRev}
	if err := s.persistWebLogin(r.Context(), snap); err != nil {
		jsonError(w, 500, "save_failed", err.Error())
		return
	}
	if err := s.limiter.SetSession(r.Context(), tok, newUser, nextRev, s.sessionTTL()); err != nil {
		jsonError(w, 503, "session_unavailable", err.Error())
		return
	}
	applog.Printf("web login credentials updated: username=%q from=%s", newUser, clientIP(r, s.trustedProxyRanges))
	json.NewEncoder(w).Encode(map[string]interface{}{
		"user": map[string]interface{}{"username": newUser},
	})
}
