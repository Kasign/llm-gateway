package server

import (
	"context"
	"testing"
	"time"
)

func TestCancelModelDownloadKeepsTaskRegisteredUntilWorkerExits(t *testing.T) {
	cancelled := make(chan struct{})
	done := make(chan struct{})
	s := &Server{
		downloadCancel: map[string]context.CancelFunc{
			"model-a": func() { close(cancelled) },
		},
		downloadDone: map[string]chan struct{}{"model-a": done},
	}

	if err := s.CancelModelDownload("model-a"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("download cancel function was not called")
	}
	s.downloadMu.Lock()
	_, stillTracked := s.downloadCancel["model-a"]
	s.downloadMu.Unlock()
	if !stillTracked {
		t.Fatal("cancel must not remove task before worker exits")
	}
	close(done)
}

func TestStopAndWaitDownloadWaitsForWorkerExit(t *testing.T) {
	cancelled := make(chan struct{})
	done := make(chan struct{})
	s := &Server{
		downloadCancel: map[string]context.CancelFunc{
			"model-a": func() { close(cancelled) },
		},
		downloadDone: map[string]chan struct{}{"model-a": done},
	}
	result := make(chan error, 1)
	go func() {
		result <- s.stopAndWaitDownload(context.Background(), "model-a")
	}()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("download cancel function was not called")
	}
	select {
	case err := <-result:
		t.Fatalf("wait returned before worker exit: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(done)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not finish after worker exit")
	}
}
