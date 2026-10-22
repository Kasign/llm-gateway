// 生成控制台 Web 登录密码哈希，写入 config.yaml 的 auth.admin_password_hash。
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/whl/llm-gateway/internal/adminuser"
)

func main() {
	plain := strings.TrimSpace(strings.Join(os.Args[1:], " "))
	if plain == "" {
		fmt.Fprintln(os.Stderr, "usage: hash-admin-password <password>")
		os.Exit(1)
	}
	h, err := adminuser.HashPassword(plain)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(h)
}
