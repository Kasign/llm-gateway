package server

import (
	"context"
	"time"
)

// contextWithOptionalTimeout 对正数时限应用整体 Deadline；0 表示不添加整体时限。
// 即使时限为 0，父 Context 取消仍会传播，例如用户主动停止 Benchmark。
func contextWithOptionalTimeout(parent context.Context, timeoutSeconds int) (context.Context, context.CancelFunc) {
	if timeoutSeconds > 0 {
		return context.WithTimeout(parent, time.Duration(timeoutSeconds)*time.Second)
	}
	return context.WithCancel(parent)
}
