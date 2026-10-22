package inferenceparams

import (
	"context"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/settings"
)

// Store loads and saves inference parameter documents.
type Store struct {
	settings *settings.Repo
	cfg      *config.Config
}

func NewStore(settings *settings.Repo, cfg *config.Config) *Store {
	return &Store{settings: settings, cfg: cfg}
}

func backendKey(backend string) string {
	if backend == "ollama" {
		return KeyOllama
	}
	return KeyLlamaCpp
}

func policyKey(backend string) string {
	if backend == "ollama" {
		return KeyPolicyOllama
	}
	return KeyPolicyLlamaCpp
}

// Load returns admin-saved params and policy only (未保存则为空；转发请用 LoadEffective).
func (s *Store) Load(ctx context.Context, backend string) (Params, Policy, error) {
	var p Params
	var pol Policy
	_, _ = s.settings.GetJSON(ctx, backendKey(backend), &p)
	ok, _ := s.settings.GetJSON(ctx, policyKey(backend), &pol)
	if !ok || pol.Mode == "" {
		// 兼容旧版全局 policy
		okLegacy, _ := s.settings.GetJSON(ctx, KeyPolicy, &pol)
		if !okLegacy || pol.Mode == "" {
			pol = DefaultPolicy()
		}
	}
	return p, pol, nil
}

func (s *Store) factoryFor(backend string) Params {
	if backend == "ollama" {
		return DefaultsOllama(s.cfg)
	}
	return DefaultsLlamaCpp(s.cfg)
}

// LoadEffective returns factory defaults merged with saved overrides (for proxy injection).
func (s *Store) LoadEffective(ctx context.Context, backend string) (Params, Policy, error) {
	saved, pol, err := s.Load(ctx, backend)
	if err != nil {
		return Params{}, pol, err
	}
	return MergeEffective(s.factoryFor(backend), saved), pol, nil
}

// Save persists params for a backend.
func (s *Store) Save(ctx context.Context, backend string, p Params) error {
	return s.settings.SetJSON(ctx, backendKey(backend), p)
}

// SavePolicy persists merge policy for a backend.
func (s *Store) SavePolicy(ctx context.Context, backend string, pol Policy) error {
	if pol.Mode == "" {
		pol.Mode = ModeClientFirst
	}
	return s.settings.SetJSON(ctx, policyKey(backend), pol)
}

// Reset replaces stored params with factory defaults for that backend only.
func (s *Store) Reset(ctx context.Context, backend string) (Params, error) {
	var p Params
	if backend == "ollama" {
		p = DefaultsOllama(s.cfg)
	} else {
		p = DefaultsLlamaCpp(s.cfg)
	}
	if err := s.Save(ctx, backend, p); err != nil {
		return Params{}, err
	}
	return p, nil
}
