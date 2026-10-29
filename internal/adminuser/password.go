package adminuser

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const minPasswordRunes = 8
const pbkdf2Iterations = 120_000
const maxPBKDF2Iterations = 1_000_000

var ErrWeakPassword = errors.New("password must be at least 8 characters")

// HashPassword 生成 PBKDF2 哈希；新格式使用冒号分隔，避免 Compose 将 $ 误作变量插值。
func HashPassword(plain string) (string, error) {
	if utf8.RuneCountInString(plain) < minPasswordRunes {
		return "", ErrWeakPassword
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk := pbkdf2Key([]byte(plain), salt, pbkdf2Iterations, 32)
	return fmt.Sprintf("pbkdf2:%d:%s:%s",
		pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(dk),
	), nil
}

// CheckPassword 校验明文与存储哈希（常量时间比较摘要）。
func CheckPassword(stored, plain string) bool {
	stored = strings.TrimSpace(stored)
	var parts []string
	switch {
	case strings.HasPrefix(stored, "pbkdf2$"):
		// 兼容数据库和旧部署配置中已保存的哈希。
		parts = strings.Split(stored, "$")
	case strings.HasPrefix(stored, "pbkdf2:"):
		parts = strings.Split(stored, ":")
	default:
		return false
	}
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 || iter > maxPBKDF2Iterations {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) != 16 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) != 32 {
		return false
	}
	got := pbkdf2Key([]byte(plain), salt, iter, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func pbkdf2Key(password, salt []byte, iter, keyLen int) []byte {
	prf := func(key, msg []byte) []byte {
		h := sha256.New()
		h.Write(key)
		h.Write(msg)
		return h.Sum(nil)
	}
	blockLen := sha256.Size
	numBlocks := (keyLen + blockLen - 1) / blockLen
	out := make([]byte, 0, numBlocks*blockLen)
	for block := 1; block <= numBlocks; block++ {
		msg := append(append([]byte{}, salt...), byte(block>>24), byte(block>>16), byte(block>>8), byte(block))
		u := prf(password, msg)
		t := append([]byte{}, u...)
		for i := 1; i < iter; i++ {
			u = prf(password, u)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}
