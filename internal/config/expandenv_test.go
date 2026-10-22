package config

import (
	"os"
	"strings"
	"testing"
)

func TestExpandConfigEnvPreservesPbkdf2Hash(t *testing.T) {
	hash := "pbkdf2$120000$Rq4omCdIiNZh1T7fMz/r1Q$Tiesjxer65K62mmSomM4uk+pRBouNU9oEnwe/Y0KeVk"
	os.Setenv("LLM_GATEWAY_ADMIN_PASSWORD_HASH", hash)
	defer os.Unsetenv("LLM_GATEWAY_ADMIN_PASSWORD_HASH")

	fromEnv := expandConfigEnv(`admin_password_hash: "${LLM_GATEWAY_ADMIN_PASSWORD_HASH}"`)
	if !strings.Contains(fromEnv, hash) {
		t.Fatalf("env substitution broken:\n%s", fromEnv)
	}

	literal := expandConfigEnv(`admin_password_hash: "` + hash + `"`)
	if literal != `admin_password_hash: "`+hash+`"` {
		t.Fatalf("literal hash corrupted:\n%s", literal)
	}
}
