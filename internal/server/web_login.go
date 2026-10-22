package server

import (
	"context"

	"github.com/whl/llm-gateway/internal/adminuser"
	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/settings"
)

type webLoginSnapshot struct {
	Username     string
	PasswordHash string
	Revision     uint64
}

func (s *Server) loadWebLoginState(ctx context.Context) {
	u := s.cfg.Auth.AdminUsername
	h := s.cfg.Auth.AdminPasswordHash
	rev := uint64(0)
	if stored, err := s.settings.GetWebLogin(ctx); err == nil && stored != nil {
		if adminuser.WebLoginConfigured(stored.Username, stored.PasswordHash) {
			u, h, rev = stored.Username, stored.PasswordHash, stored.Revision
		} else {
			applog.Printf("web login: ignoring invalid web_admin_login in database, using config defaults")
		}
	}
	s.webLoginMu.Lock()
	s.webLogin = webLoginSnapshot{Username: u, PasswordHash: h, Revision: rev}
	s.webLoginMu.Unlock()
}

func (s *Server) effectiveWebLogin() (username, passwordHash string, revision uint64, ok bool) {
	s.webLoginMu.RLock()
	defer s.webLoginMu.RUnlock()
	if !adminuser.WebLoginConfigured(s.webLogin.Username, s.webLogin.PasswordHash) {
		return "", "", 0, false
	}
	return s.webLogin.Username, s.webLogin.PasswordHash, s.webLogin.Revision, true
}

func (s *Server) sessionMatchesWebLogin(username string, revision uint64) bool {
	u, _, rev, ok := s.effectiveWebLogin()
	if !ok {
		return false
	}
	return username == u && revision == rev
}

func (s *Server) persistWebLogin(ctx context.Context, snap webLoginSnapshot) error {
	if err := s.settings.SaveWebLogin(ctx, settings.WebLoginStored{
		Username:     snap.Username,
		PasswordHash: snap.PasswordHash,
		Revision:     snap.Revision,
	}); err != nil {
		return err
	}
	s.webLoginMu.Lock()
	s.webLogin = snap
	s.webLoginMu.Unlock()
	return nil
}
