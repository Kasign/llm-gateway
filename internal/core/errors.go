// core 的失败带稳定的 HTTP 状态和错误码，调用方不必再解析错误文案。
package core

import (
	"context"
	"errors"
)

// Error 是主流程可识别的失败。观察者和 HTTP 适配都从这里取状态码。
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// Unpack 把错误还原成对外状态码。取消使用 499，与现有 benchmark 停止记录一致。
func Unpack(err error) (status int, code, msg string) {
	if err == nil {
		return 200, "", ""
	}
	if errors.Is(err, context.Canceled) {
		return 499, "cancelled", "request cancelled"
	}
	var ce *Error
	if errors.As(err, &ce) && ce != nil {
		status = ce.Status
		if status == 0 {
			status = 502
		}
		code = ce.Code
		if code == "" {
			code = "error"
		}
		return status, code, ce.Message
	}
	return 502, "upstream_error", err.Error()
}
