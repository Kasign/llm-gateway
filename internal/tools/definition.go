// Package tools 提供 Gateway 内部工具的公共契约、参数校验与注册能力。
//
// 所有具体工具统一归属 internal/tools，并在 modules 子目录中按独立 Go
// package 组织。工具包不依赖 server、provider 或 store。
package tools

import (
	"context"
	"encoding/json"
)

// Definition 描述一个稳定的工具能力，不包含运行时地址、凭据或配置指纹。
type Definition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Clone 返回深拷贝，避免调用方修改 Registry 内部保存的 Schema。
func (d Definition) Clone() Definition {
	d.Parameters = append(json.RawMessage(nil), d.Parameters...)
	return d
}

// Executor 是具体工具子模块必须实现的最小执行契约。
// 参数和结果均为 JSON；安全限制由后续 Tool Runtime 统一负责。
type Executor interface {
	Execute(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error)
}
