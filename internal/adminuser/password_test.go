package adminuser

import (
	"strings"
	"testing"
)

func TestCheckPasswordPlaceholderHash(t *testing.T) {
	legacyHash := "pbkdf2$120000$Rq4omCdIiNZh1T7fMz/r1Q$Tiesjxer65K62mmSomM4uk+pRBouNU9oEnwe/Y0KeVk"
	if !CheckPassword(legacyHash, "changeme123") {
		t.Fatal("expected changeme123 to match legacy placeholder hash")
	}

	currentHash := strings.ReplaceAll(legacyHash, "$", ":")
	if !CheckPassword(currentHash, "changeme123") {
		t.Fatal("expected changeme123 to match colon-delimited placeholder hash")
	}

	generated, err := HashPassword("a-strong-test-password")
	if err != nil {
		t.Fatalf("HashPassword returned an error: %v", err)
	}
	if strings.Contains(generated, "$") {
		t.Fatalf("generated hash must not contain Compose interpolation characters: %q", generated)
	}
	if !CheckPassword(generated, "a-strong-test-password") {
		t.Fatal("generated hash should match its password")
	}
}
