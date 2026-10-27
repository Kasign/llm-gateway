package config

import "testing"

func TestApplyAuthEnvWebLoginResetFromEnv(t *testing.T) {
	t.Setenv("LLM_GATEWAY_WEB_LOGIN_RESET_FROM_ENV", "true")
	var cfg Config
	if err := applyAuthEnv(&cfg); err != nil { t.Fatalf("applyAuthEnv returned error: %v", err) }
	if !cfg.Auth.WebLoginResetFromEnv { t.Fatal("expected reset-from-env to be enabled") }
}

func TestApplyAuthEnvRejectsInvalidWebLoginResetFromEnv(t *testing.T) {
	t.Setenv("LLM_GATEWAY_WEB_LOGIN_RESET_FROM_ENV", "maybe")
	var cfg Config
	if err := applyAuthEnv(&cfg); err == nil { t.Fatal("expected invalid reset-from-env value to be rejected") }
}
