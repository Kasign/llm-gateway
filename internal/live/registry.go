package live

import (
	"sync"

	"github.com/whl/llm-gateway/internal/store"
)

// Registry 进程内进行中的调用记录索引。
type Registry struct {
	mu sync.RWMutex
	m  map[string]*Session
}

func NewRegistry() *Registry {
	return &Registry{m: make(map[string]*Session)}
}

func (r *Registry) Register(l store.RequestLog) *Session {
	if r == nil || l.RequestID == "" {
		return nil
	}
	if l.Status == "" {
		l.Status = "running"
	}
	s := &Session{Log: l}
	r.mu.Lock()
	r.m[l.RequestID] = s
	r.mu.Unlock()
	return s
}

func (r *Registry) Get(id string) *Session {
	if r == nil || id == "" {
		return nil
	}
	r.mu.RLock()
	s := r.m[id]
	r.mu.RUnlock()
	return s
}

func (r *Registry) Unregister(id string) {
	if r == nil || id == "" {
		return
	}
	r.mu.Lock()
	delete(r.m, id)
	r.mu.Unlock()
}

// Clear 清空全部进行中快照（例如管理端清空调用记录时）。
func (r *Registry) Clear() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.m = make(map[string]*Session)
	r.mu.Unlock()
}

func (r *Registry) ListSummaries() []store.RequestLog {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	sessions := make([]*Session, 0, len(r.m))
	for _, s := range r.m {
		if s != nil {
			sessions = append(sessions, s)
		}
	}
	r.mu.RUnlock()
	out := make([]store.RequestLog, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.LogSummary())
	}
	return out
}

func (r *Registry) SnapshotDetail(id string) *DetailSnapshot {
	s := r.Get(id)
	if s == nil {
		return nil
	}
	snap := s.SnapshotDetail()
	return &snap
}
