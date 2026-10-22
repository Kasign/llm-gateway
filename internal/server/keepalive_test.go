package server

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/config"
	"github.com/whl/llm-gateway/internal/live"
	"github.com/whl/llm-gateway/internal/store"
)

func TestRepeatKeepaliveStopsOnCancel(t *testing.T) {
	sink := newMemSink()
	ctx, cancel := context.WithCancel(context.Background())
	line := ": keepalive\n\n"
	stop := repeatKeepalive(ctx, "req-test", sink, line, 15*time.Millisecond, nil, nil)
	defer stop()
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) && bytes.Count(sink.Bytes(), []byte(line)) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if bytes.Count(sink.Bytes(), []byte(line)) < 2 {
		t.Fatalf("writes %d", bytes.Count(sink.Bytes(), []byte(line)))
	}
	cancel()
	time.Sleep(50 * time.Millisecond)
	stopped := bytes.Count(sink.Bytes(), []byte(line))
	time.Sleep(80 * time.Millisecond)
	if later := bytes.Count(sink.Bytes(), []byte(line)); later != stopped {
		t.Fatalf("kept writing after cancel: %d -> %d", stopped, later)
	}
}

func TestRepeatKeepaliveStopsOnWriteError(t *testing.T) {
	sink := &failAfterSink{failAt: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var notes int
	var failed error
	stop := repeatKeepalive(ctx, "req-test", sink, ": keepalive\n\n", 10*time.Millisecond, func(int) {
		notes++
	}, func(err error) {
		failed = err
	})
	defer stop()
	time.Sleep(80 * time.Millisecond)
	if n := sink.writes(); n != 1 {
		t.Fatalf("writes %d", n)
	}
	if notes != 0 {
		t.Fatalf("recorded failed write: %d", notes)
	}
	if failed == nil {
		t.Fatal("expected write failure")
	}
}

func TestRepeatKeepaliveRecordsEachWrite(t *testing.T) {
	sink := newMemSink()
	ctx := context.Background()
	line := ": keepalive\n\n"
	var mu sync.Mutex
	var notes []int
	stop := repeatKeepalive(ctx, "req-test", sink, line, 12*time.Millisecond, func(n int) {
		mu.Lock()
		notes = append(notes, n)
		mu.Unlock()
	}, nil)
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(notes)
		mu.Unlock()
		if got >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	got := append([]int(nil), notes...)
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("notes %d", len(got))
	}
	for _, n := range got {
		if n != len(line) {
			t.Fatalf("note bytes %d", n)
		}
	}
	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	later := len(notes)
	mu.Unlock()
	if later != len(got) {
		t.Fatalf("kept noting after stop: %d -> %d", len(got), later)
	}
}

func TestNoteKeepaliveRecordsEachWrite(t *testing.T) {
	// 此用例显式开启原始 SSE 留存，验证开启诊断后仍保留 Gateway 自己的保活行。
	cfg := &config.Config{}
	cfg.Logging.StoreStreamRaw = true
	s := &Server{cfg: cfg, live: live.NewRegistry()}
	const id = "req-keepalive"
	s.callBegin(id, nil)
	s.liveStart(context.Background(), store.RequestLog{RequestID: id, Status: "running", StartedAt: time.Now()})
	cs := s.callGet(id)
	cs.mu.Lock()
	cs.trace = s.attachRequestTrace(id, time.Now())
	cs.mu.Unlock()

	line := ": keepalive\n\n"
	s.noteKeepalive(id, len(line), line)
	s.noteKeepalive(id, len(line), line)
	evs := cs.trace.drainEvents()
	if len(evs) != 2 {
		t.Fatalf("events %d", len(evs))
	}
	if evs[0].Kind != "keepalive" || evs[1].Kind != "keepalive" {
		t.Fatalf("kinds %s %s", evs[0].Kind, evs[1].Kind)
	}
	if evs[0].Seq == evs[1].Seq || evs[0].RawLine != line || evs[1].RawLine != line {
		t.Fatalf("%+v %+v", evs[0], evs[1])
	}
	if evs[0].AtMS > evs[1].AtMS {
		t.Fatalf("order %d %d", evs[0].AtMS, evs[1].AtMS)
	}
}

type failAfterSink struct {
	mu     sync.Mutex
	n      int
	failAt int
}

func (s *failAfterSink) SetHeader(string, string) {}
func (s *failAfterSink) WriteHeader(int)          {}
func (s *failAfterSink) Flush()                   {}
func (s *failAfterSink) HeaderWritten() bool      { return false }

func (s *failAfterSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	if s.n >= s.failAt {
		return 0, errors.New("closed")
	}
	return len(p), nil
}

func (s *failAfterSink) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}
