package server

import (
	"context"
	"sync"
)

// runCancels 登记进行中的推理取消函数。管理端结束调用和 benchmark 停止都走这里。
// Run 本身不知道谁在取消，它只遵守传入的 context。
type runCancels struct {
	mu sync.Mutex
	m  map[string]context.CancelFunc
}

func newRunCancels() *runCancels {
	return &runCancels{m: map[string]context.CancelFunc{}}
}

func (r *runCancels) Register(id string, cancel context.CancelFunc) {
	if r == nil || id == "" || cancel == nil {
		return
	}
	r.mu.Lock()
	r.m[id] = cancel
	r.mu.Unlock()
}

func (r *runCancels) Unregister(id string) {
	if r == nil || id == "" {
		return
	}
	r.mu.Lock()
	delete(r.m, id)
	r.mu.Unlock()
}

// Cancel 取消这条进行中的推理。请求已经结束时返回 false。
func (r *runCancels) Cancel(id string) bool {
	if r == nil || id == "" {
		return false
	}
	r.mu.Lock()
	cancel := r.m[id]
	r.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}
