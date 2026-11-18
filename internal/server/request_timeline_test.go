package server

import (
	"errors"
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRequestTimelineRecordsMonotonicOffsetsAndCopiesSnapshot(t *testing.T) {
	start := time.Now()
	timeline := newRequestTimeline(start)
	timeline.record("request_received", start)
	timeline.record("auth_started", start.Add(12*time.Millisecond))
	timeline.markIfMissing("body_read_finished", timelineStatusNotReached)
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
	timeline.markIfMissing("ready", timelineStatusMeasured)
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
	timeline.recordNow("handler_exit_started")
	events := timeline.snapshot()
	if len(events) != 2 || events[1].Name != "handler_exit_started" {
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

type failedTimelineResponseWriter struct {
	header http.Header
}

func (w *failedTimelineResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failedTimelineResponseWriter) WriteHeader(int) {}

func (w *failedTimelineResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestResponseSinkRecordsFirstAndLastSuccessfulWrites(t *testing.T) {
	start := time.Now()
	timeline := newRequestTimeline(start)
	recorder := httptest.NewRecorder()
	sink := &responseSink{w: recorder, timeline: timeline}

	sink.WriteHeader(http.StatusOK)
	if _, err := sink.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.Write([]byte("last")); err != nil {
		t.Fatal(err)
	}
	if recorder.Body.String() != "firstlast" {
		t.Fatalf("unexpected response body: %q", recorder.Body.String())
	}
	events := timeline.snapshot()
	if len(events) != 2 {
		t.Fatalf("expected first and last write events, got %+v", events)
	}
	if events[0].Name != "gateway_first_write" || events[1].Name != "gateway_last_write" {
		t.Fatalf("unexpected write event order: %+v", events)
	}
	if events[0].OffsetMS == nil || events[1].OffsetMS == nil ||
		*events[1].OffsetMS < *events[0].OffsetMS {
		t.Fatalf("write events must contain ordered measured offsets: %+v", events)
	}
}

func TestResponseSinkDoesNotRecordFailedBodyWrite(t *testing.T) {
	timeline := newRequestTimeline(time.Now())
	sink := &responseSink{w: &failedTimelineResponseWriter{}, timeline: timeline}
	if _, err := sink.Write([]byte("unwritten")); err == nil {
		t.Fatal("expected write failure")
	}
	if events := timeline.snapshot(); len(events) != 0 {
		t.Fatalf("failed write must not be recorded as successful: %+v", events)
	}
}


type hijackTimelineResponseWriter struct {
	header http.Header
	conn net.Conn
}

func (w *hijackTimelineResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *hijackTimelineResponseWriter) WriteHeader(int) {}
func (w *hijackTimelineResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *hijackTimelineResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

func TestResponseSinkUnwrapPreservesResponseControllerCapabilities(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	underlying := &hijackTimelineResponseWriter{conn: serverConn}
	sink := &responseSink{w: underlying}
	conn, rw, err := http.NewResponseController(sink).Hijack()
	if err != nil {
		t.Fatalf("ResponseController.Hijack() failed through responseSink: %v", err)
	}
	if conn != serverConn || rw == nil {
		t.Fatalf("unexpected hijack result: conn=%v rw=%v", conn, rw)
	}
}
