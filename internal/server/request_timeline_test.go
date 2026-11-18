package server

import (
	"sync"
	"testing"
	"time"
)

func TestRequestTimelineRecordsMonotonicOffsetsAndCopiesSnapshot(t *testing.T) {
	start := time.Now()
	timeline := newRequestTimeline(start)
	timeline.record("request_received", start)
	timeline.record("auth_started", start.Add(12*time.Millisecond))
	timeline.mark("body_read_finished", timelineStatusNotReached)
	first := timeline.snapshot()
	if len(first) != 3 {
		t.Fatalf("unexpected event count: %d", len(first))
	}
	if first[0].OffsetMS == nil || *first[0].OffsetMS != 0 {
		t.Fatalf("zero offset must be a valid measured event: %+v", first[0])
	}
	if first[1].OffsetMS == nil || *first[1].OffsetMS != 12 {
		t.Fatalf("unexpected relative offset: %+v", first[1])
	}
	if first[2].OffsetMS != nil || first[2].Status != timelineStatusNotReached {
		t.Fatalf("not-reached event must not invent an offset: %+v", first[2])
	}
	*first[0].OffsetMS = 999
	if got := timeline.snapshot()[0].OffsetMS; got == nil || *got != 0 {
		t.Fatalf("snapshot must not expose internal pointers: %v", got)
	}
}

func TestRequestTimelineRejectsUnknownAndOutOfRangeEvents(t *testing.T) {
	start := time.Now()
	timeline := newRequestTimeline(start)
	timeline.record("made_up_event", start.Add(time.Second))
	timeline.record("auth_started", start.Add(-time.Second))
	timeline.mark("ready", timelineStatusMeasured)
	for i := 0; i < requestTimelineMaxEvents+5; i++ {
		timeline.recordNow("pipeline_started")
	}
	if got := len(timeline.snapshot()); got != requestTimelineMaxEvents {
		t.Fatalf("event bound not enforced: %d", got)
	}
}

func TestRequestTimelineConcurrentRecordingIsBounded(t *testing.T) {
	timeline := newRequestTimeline(time.Now())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				timeline.recordNow("gateway_last_write")
			}
		}()
	}
	wg.Wait()
	if got := len(timeline.snapshot()); got != requestTimelineMaxEvents {
		t.Fatalf("concurrent event bound not enforced: %d", got)
	}
}

func TestRequestTimelineDefersAnalysisSnapshotUntilHandlerExit(t *testing.T) {
	timeline := newRequestTimeline(time.Now())
	timeline.recordNow("request_received")
	timeline.setAnalysis(analysisInput{TotalMS: 15})
	if _, ok := timeline.analysisSnapshot(); !ok {
		t.Fatal("analysis snapshot was not retained")
	}
	timeline.recordNow("handler_returned")
	events := timeline.snapshot()
	if len(events) != 2 || events[1].Name != "handler_returned" {
		t.Fatalf("handler exit event missing or unordered: %+v", events)
	}
}

func TestRequestTimelineMarkIfMissingDoesNotDuplicateMeasuredEvents(t *testing.T) {
	start := time.Now()
	timeline := newRequestTimeline(start)
	timeline.recordFirst("upstream_headers", start.Add(time.Millisecond))
	timeline.markIfMissing("upstream_headers", timelineStatusNotReached)
	timeline.markIfMissing("upstream_first_byte", timelineStatusNotReached)
	events := timeline.snapshot()
	if len(events) != 2 {
		t.Fatalf("unexpected duplicate status event: %+v", events)
	}
	if events[0].Status != timelineStatusMeasured || events[1].Status != timelineStatusNotReached {
		t.Fatalf("measured event was overwritten: %+v", events)
	}
}
