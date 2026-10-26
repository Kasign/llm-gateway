package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/whl/llm-gateway/internal/config"
)

func TestProviderAPIKeyEncryptionRoundTripAndRejectsWrongKey(t *testing.T) {
	key := bytes.Repeat([]byte{0x4a}, 32)
	secret := "test-provider-secret"
	ciphertext, err := encryptProviderAPIKey(key, secret)
	if err != nil { t.Fatal(err) }
	if ciphertext == secret || strings.Contains(ciphertext, secret) { t.Fatal("stored representation contains plaintext key") }
	got, err := decryptProviderAPIKey(key, ciphertext)
	if err != nil { t.Fatal(err) }
	if got != secret { t.Fatalf("round trip = %q", got) }
	otherKey := bytes.Repeat([]byte{0x3b}, 32)
	if _, err := decryptProviderAPIKey(otherKey, ciphertext); err == nil { t.Fatal("wrong encryption key must be rejected") }
}

func TestSafeProviderViewNeverExposesPlaintextOrCiphertext(t *testing.T) {
	s := &Server{}
	view := s.safeProviderView(managedProvider{ID:"remote", Enabled:true, APIKey:"plaintext-secret", APIKeyCiphertext:"v1:ciphertext-secret"}, "web", true)
	raw, err := json.Marshal(view); if err != nil { t.Fatal(err) }
	text := string(raw)
	for _, forbidden := range []string{"plaintext-secret","ciphertext-secret","api_key_ciphertext",`"api_key"`} { if strings.Contains(text,forbidden) { t.Fatalf("Provider view leaked %q: %s",forbidden,text) } }
	if view["api_key_configured"] != true { t.Fatal("safe view should disclose whether a key exists") }
}

func TestDeploymentProviderIDIncludesDisabledEntries(t *testing.T) {
	s := &Server{cfg:&config.Config{}}
	s.cfg.Providers.OpenAICompatible = []config.OpenAICompatibleProviderConfig{{ID:"reserved-remote",Enabled:false}}
	if !s.deploymentProviderID("RESERVED-REMOTE") { t.Fatal("disabled deployment Provider ID must remain reserved") }
	if s.deploymentProviderID("other") { t.Fatal("unconfigured Provider ID must not be reserved") }
}
