package adminuser

import (
	"strings"
	"testing"
)

func TestCheckPasswordPlaceholderHash(t *testing.T) {
	defaultHash := "pbkdf2:120000:NPQtPMgg+xqbAp04ObNRnA:J9q8Ggs9XtRIZ1E6Fb6Q9anZ0wcYPz/5zFX26GfrZjQ"
	if !CheckPassword(defaultHash, "999888777") {
		t.Fatal("expected default hash to match 999888777")
	}
	if CheckPassword(defaultHash, "changeme123") {
		t.Fatal("default hash must not match the previous placeholder password")
	}

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
