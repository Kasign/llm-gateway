package server

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/config"
)

func TestRequestRetentionRuntimeStatusTracksSuccessAndFailure(t *testing.T) {
	server := &Server{}
	start := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	server.beginRequestRetentionRun(start)
	server.finishRequestRetentionRun(start, start.Add(1250*time.Millisecond), 4, nil)

	first := server.requestRetentionRuntimeSnapshot()
	if first.LastRunStatus != "success" || first.RunCount != 1 || first.SuccessCount != 1 {
		t.Fatalf("unexpected successful run status: %+v", first)
	}
	if first.LastRunDurationMS != 1250 || first.LastRunDeletedRows != 4 || first.TotalDeletedRows != 4 {
		t.Fatalf("unexpected successful run metrics: %+v", first)
	}

	secondStart := start.Add(time.Hour)
	server.beginRequestRetentionRun(secondStart)
	server.finishRequestRetentionRun(secondStart, secondStart.Add(50*time.Millisecond), 2, errors.New("sensitive MariaDB diagnostic"))
	second := server.requestRetentionRuntimeSnapshot()
	if second.LastRunStatus != "failed" || second.RunCount != 2 || second.FailureCount != 1 {
		t.Fatalf("unexpected failed run status: %+v", second)
	}
	if second.LastFailureCode != "cleanup_failed" || second.LastFailureAt == "" || second.TotalDeletedRows != 6 {
		t.Fatalf("unexpected failure metadata: %+v", second)
	}
	encoded, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sensitive MariaDB diagnostic") {
		t.Fatalf("runtime status exposed raw database error: %s", encoded)
	}
}

func TestRequestRetentionStatusReflectsDisabledAndUnavailable(t *testing.T) {
	cfg := &config.Config{}
	cfg.Logging.RetentionBatchSize = 500
	cfg.Logging.RetentionIntervalSeconds = 3600
	server := &Server{cfg: cfg}
	disabled := server.requestRetentionStatusSnapshot()
	if disabled.Enabled || disabled.Runtime.LastRunStatus != "disabled" {
		t.Fatalf("unexpected disabled status: %+v", disabled)
	}
	cfg.Logging.RetentionDays = 7
	unavailable := server.requestRetentionStatusSnapshot()
	if !unavailable.Enabled || unavailable.Runtime.LastRunStatus != "unavailable" {
		t.Fatalf("unexpected unavailable status: %+v", unavailable)
	}
}
