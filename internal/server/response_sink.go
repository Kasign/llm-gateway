package server

import (
	"net/http"
	"sync"
)

// responseSink 同时满足 net/http 与 core.ResponseSink。
// 保活一旦写出，之后的 WriteHeader 不再改状态行。
type responseSink struct {
	w     http.ResponseWriter
	mu    sync.Mutex
	wrote bool
}

func (s *responseSink) Header() http.Header { return s.w.Header() }

func (s *responseSink) SetHeader(key, value string) {
	s.w.Header().Set(key, value)
}

func (s *responseSink) WriteHeader(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wrote {
		return
	}
	s.wrote = true
	s.w.WriteHeader(status)
}

func (s *responseSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.wrote {
		s.wrote = true
	}
	// 持锁写完一整块。保活 goroutine 和上游转发会并发写同一连接，不能在解锁后再写。
	return s.w.Write(p)
}

func (s *responseSink) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *responseSink) HeaderWritten() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wrote
}

// memSink 给没有 HTTP 客户端的进程内调用（benchmark）收集上游输出。
type memSink struct {
	mu     sync.Mutex
	header http.Header
	body   []byte
	status int
	wrote  bool
}

func newMemSink() *memSink {
	return &memSink{header: make(http.Header)}
}

func (m *memSink) Header() http.Header { return m.header }

func (m *memSink) SetHeader(key, value string) { m.header.Set(key, value) }

func (m *memSink) WriteHeader(status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wrote {
		return
	}
	m.wrote = true
	m.status = status
}

func (m *memSink) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.wrote {
		m.wrote = true
		if m.status == 0 {
			m.status = http.StatusOK
		}
	}
	m.body = append(m.body, p...)
	return len(p), nil
}

func (m *memSink) Flush() {}

func (m *memSink) HeaderWritten() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.wrote
}

func (m *memSink) Bytes() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.body...)
}
