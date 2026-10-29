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

func TestWebLoginConfiguredRejectsMalformedHash(t *testing.T) {
	tests := []struct {
		name string
		hash string
		want bool
	}{
		{name: "有效冒号格式", hash: "pbkdf2:120000:NPQtPMgg+xqbAp04ObNRnA:J9q8Ggs9XtRIZ1E6Fb6Q9anZ0wcYPz/5zFX26GfrZjQ", want: true},
		{name: "有效旧美元格式", hash: "pbkdf2$120000$Rq4omCdIiNZh1T7fMz/r1Q$Tiesjxer65K62mmSomM4uk+pRBouNU9oEnwe/Y0KeVk", want: true},
		{name: "只有格式前缀", hash: "pbkdf2:broken"},
		{name: "错误迭代次数", hash: "pbkdf2:0:NPQtPMgg+xqbAp04ObNRnA:J9q8Ggs9XtRIZ1E6Fb6Q9anZ0wcYPz/5zFX26GfrZjQ"},
		{name: "无效盐值", hash: "pbkdf2:120000:%%%:J9q8Ggs9XtRIZ1E6Fb6Q9anZ0wcYPz/5zFX26GfrZjQ"},
		{name: "摘要长度错误", hash: "pbkdf2:120000:NPQtPMgg+xqbAp04ObNRnA:YWJj"},
		{name: "字段不完整", hash: "pbkdf2:120000:NPQtPMgg+xqbAp04ObNRnA"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WebLoginConfigured("admin", tt.hash); got != tt.want {
				t.Fatalf("WebLoginConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
	if WebLoginConfigured("", "pbkdf2:120000:NPQtPMgg+xqbAp04ObNRnA:J9q8Ggs9XtRIZ1E6Fb6Q9anZ0wcYPz/5zFX26GfrZjQ") {
		t.Fatal("empty username must not be considered configured")
	}
}

func TestCheckPasswordRejectsMalformedOrExpensiveHashes(t *testing.T) {
	tests := []struct {
		name string
		hash string
	}{
		{name: "空摘要不得匹配任意密码", hash: "pbkdf2:120000:NPQtPMgg+xqbAp04ObNRnA:"},
		{name: "超大迭代次数必须拒绝", hash: "pbkdf2:999999999:NPQtPMgg+xqbAp04ObNRnA:J9q8Ggs9XtRIZ1E6Fb6Q9anZ0wcYPz/5zFX26GfrZjQ"},
		{name: "盐值长度错误必须拒绝", hash: "pbkdf2:120000:YQ:J9q8Ggs9XtRIZ1E6Fb6Q9anZ0wcYPz/5zFX26GfrZjQ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if CheckPassword(tt.hash, "any-password") {
				t.Fatal("malformed hash must never match")
			}
		})
	}
}
