package server

import (
	"context"
	"sync"
)

// runEntry 同时保存取消函数和请求阶段；finishing 与 cancelRequested 在同一把锁下线性化。
type runEntry struct {
	cancel          context.CancelFunc
	finishing       bool
	cancelRequested bool
}

// runCancels 登记进行中的推理取消函数。管理端结束调用和 benchmark 停止都走这里。
type runCancels struct {
	mu sync.Mutex
	m  map[string]runEntry
}

func newRunCancels() *runCancels {
	return &runCancels{m: map[string]runEntry{}}
}

func (r *runCancels) Register(id string, cancel context.CancelFunc) {
	if r == nil || id == "" || cancel == nil {
		return
	}
	r.mu.Lock()
	r.m[id] = runEntry{cancel: cancel}
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

// MarkFinishing 关闭新的取消入口，并返回此前是否有取消已被接受。
func (r *runCancels) MarkFinishing(id string) bool {
	if r == nil || id == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.m[id]
	if !ok {
		return false
	}
	entry.finishing = true
	r.m[id] = entry
	return entry.cancelRequested
}

func (r *runCancels) IsFinishing(id string) bool {
	if r == nil || id == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.m[id]
	return ok && entry.finishing
}

// Cancel 取消这条仍处于活动阶段的推理。Finish 先获得锁后，迟到的取消会返回 false。
func (r *runCancels) Cancel(id string) bool {
	if r == nil || id == "" {
		return false
	}
	r.mu.Lock()
	entry, ok := r.m[id]
	if !ok || entry.finishing || entry.cancel == nil {
		r.mu.Unlock()
		return false
	}
	entry.cancelRequested = true
	cancel := entry.cancel
	r.m[id] = entry
	r.mu.Unlock()

	cancel()
	return true
}
