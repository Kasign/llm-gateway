package adminuser

import "testing"

func TestCheckPasswordPlaceholderHash(t *testing.T) {
	hash := "pbkdf2$120000$Rq4omCdIiNZh1T7fMz/r1Q$Tiesjxer65K62mmSomM4uk+pRBouNU9oEnwe/Y0KeVk"
	if !CheckPassword(hash, "changeme123") {
		t.Fatal("expected changeme123 to match placeholder hash")
	}
}
