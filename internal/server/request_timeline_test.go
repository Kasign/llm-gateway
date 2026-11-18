package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/provider"
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
	events := timeline.snapshot()
	if len(events) != 1 || events[0].Name != "gateway_write_failed" ||
		events[0].Status != timelineStatusMeasured || events[0].OffsetMS == nil {
		t.Fatalf("failed write must be recorded as a failure, not a successful write: %+v", events)
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


type shortWriteTimelineResponseWriter struct {
	header http.Header
}

func (w *shortWriteTimelineResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *shortWriteTimelineResponseWriter) WriteHeader(int) {}
func (w *shortWriteTimelineResponseWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

func TestResponseSinkRecordsPartialWriteAsSuccessAndFailure(t *testing.T) {
	timeline := newRequestTimeline(time.Now())
	sink := &responseSink{w: &shortWriteTimelineResponseWriter{}, timeline: timeline}
	payload := []byte("partial")
	n, err := sink.Write(payload)
	if err != nil || n != len(payload)-1 {
		t.Fatalf("unexpected short write result: n=%d err=%v", n, err)
	}
	events := timeline.snapshot()
	if len(events) != 3 {
		t.Fatalf("expected first, last and failed write events, got %+v", events)
	}
	want := []string{"gateway_first_write", "gateway_last_write", "gateway_write_failed"}
	for i, name := range want {
		if events[i].Name != name || events[i].Status != timelineStatusMeasured || events[i].OffsetMS == nil {
			t.Fatalf("event %d = %+v, want measured %s", i, events[i], name)
		}
	}
}


type interruptedStreamResponseWriter struct {
	header http.Header
	writes int
}

func (w *interruptedStreamResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *interruptedStreamResponseWriter) WriteHeader(int) {}
func (w *interruptedStreamResponseWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 1 {
		return len(p), nil
	}
	return 0, errors.New("client connection closed")
}

type interruptedStreamForwarder struct{}

func (interruptedStreamForwarder) Forward(_ context.Context, st core.State, _ core.ChunkFunc) (core.Result, error) {
	sink, ok := st.Sink.(http.ResponseWriter)
	if !ok {
		return core.Result{}, errors.New("response sink does not implement http.ResponseWriter")
	}
	sink.WriteHeader(http.StatusOK)
	if _, err := sink.Write([]byte("first chunk")); err != nil {
		return core.Result{}, err
	}
	if _, err := sink.Write([]byte("interrupted chunk")); err != nil {
		return core.Result{}, err
	}
	return core.Result{}, nil
}


func TestInferenceForwarderRecordsRealProviderHTTPErrorHeaders(t *testing.T) {
	const privateBody = "upstream-private-diagnostic"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"` + privateBody + `"}`))
	}))
	defer upstream.Close()

	remote, err := provider.NewOpenAICompatibleProvider(provider.OpenAICompatibleConfig{
		ID: "timeline-provider", BaseURL: upstream.URL + "/v1",
		AllowInsecureHTTP: true, AllowPrivateNetwork: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := provider.NewRegistry()
	if err := registry.Register("timeline-provider", func() provider.Provider { return remote }); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	timeline := newRequestTimeline(start)
	ctx := context.WithValue(context.Background(), requestTimelineContextKey{}, timeline)
	recorder := httptest.NewRecorder()
	sink := &responseSink{w: recorder, method: http.MethodPost, timeline: timeline}
	forwarder := inferenceForwarder{s: &Server{providers: registry}}
	result, forwardErr := forwarder.Forward(ctx, core.State{
		Request: core.Request{
			ID: "timeline-provider-http-error", Method: http.MethodPost,
			Path: "/v1/chat/completions", Body: []byte("{}"), Started: start, Sink: sink,
		},
		Model: core.ModelChoice{ProviderID: "timeline-provider", UpstreamID: "upstream-model"},
	}, nil)
	if forwardErr == nil {
		t.Fatal("expected Provider HTTP error")
	}
	if result.Status != http.StatusBadGateway || result.UpstreamStatus != http.StatusBadGateway {
		t.Fatalf("forward result = %+v", result)
	}
	events := make(map[string]requestTimelineEvent)
	for _, event := range timeline.snapshot() {
		events[event.Name] = event
	}
	headers, ok := events["upstream_headers"]
	if !ok || headers.Status != timelineStatusMeasured || headers.OffsetMS == nil {
		t.Fatalf("real upstream response headers were not recorded: %+v", timeline.snapshot())
	}
	if firstByte, ok := events["upstream_first_byte"]; !ok || firstByte.Status != timelineStatusMeasured || firstByte.OffsetMS == nil {
		t.Fatalf("first upstream response-body byte was not measured: %+v", timeline.snapshot())
	}
	if !timeline.has("gateway_first_write") || !timeline.has("gateway_last_write") {
		t.Fatalf("sanitized provider error response was not recorded as a gateway write: %+v", timeline.snapshot())
	}
	if strings.Contains(recorder.Body.String(), privateBody) {
		t.Fatalf("upstream error body leaked to caller: %q", recorder.Body.String())
	}
}

func TestRequestTimelineRecordsStreamInterruptionAfterHeaders(t *testing.T) {
	start := time.Now()
	timeline := newRequestTimeline(start)
	ctx := context.WithValue(context.Background(), requestTimelineContextKey{}, timeline)
	writer := &interruptedStreamResponseWriter{}
	sink := &responseSink{w: writer, method: http.MethodPost, timeline: timeline}
	pipe := &core.Pipeline{
		Resolve: func(context.Context, core.State) (core.ModelChoice, error) {
			return core.ModelChoice{ProviderID: "test", UpstreamID: "test-model"}, nil
		},
		Forward: interruptedStreamForwarder{},
		Observe: timeline,
	}
	_, err := pipe.Run(ctx, core.Request{
		ID: "timeline-stream-interrupted", Method: http.MethodPost, Path: "/v1/chat/completions",
		Body: []byte(`{"model":"test-model","messages":[],"stream":true}`),
		Started: start, Sink: sink,
	})
	if err == nil {
		t.Fatal("expected interrupted stream write to fail")
	}
	got := make(map[string]bool)
	for _, event := range timeline.snapshot() {
		got[event.Name] = event.Status == timelineStatusMeasured && event.OffsetMS != nil
	}
	for _, name := range []string{"gateway_first_write", "gateway_last_write", "gateway_write_failed", "pipeline_finished"} {
		if !got[name] {
			t.Fatalf("missing measured %s after stream interruption: %+v", name, timeline.snapshot())
		}
	}
	if writer.writes != 2 {
		t.Fatalf("unexpected underlying write count: %d", writer.writes)
	}
}


type timelineSmallSendBufferListener struct {
	net.Listener
}

func (l timelineSmallSendBufferListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetWriteBuffer(4096)
	}
	return conn, nil
}

func TestResponseSinkRecordsRealTCPClientDisconnect(t *testing.T) {\n\tdisconnectTimelineResults := make(chan []requestTimelineEvent, 1)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timeline := newRequestTimeline(time.Now())
		sink := &responseSink{w: w, method: r.Method, timeline: timeline}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "67108864")
		sink.WriteHeader(http.StatusOK)
		if _, writeErr := sink.Write([]byte("ready")); writeErr != nil {
			t.Errorf("initial response write failed: %v", writeErr)
			return
		}
		sink.Flush()

		<-r.Context().Done()
		chunk := make([]byte, 1<<20)
		for i := 0; i < 64; i++ {
			n, writeErr := sink.Write(chunk)
			if writeErr != nil || n != len(chunk) {
				break
			}
		}
		events := timeline.snapshot()
		disconnectTimelineResults <- events
	})}
	defer server.Close()
	go func() { _ = server.Serve(timelineSmallSendBufferListener{Listener: listener}) }()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		conn.Close()
		t.Fatalf("read response status: %v", err)
	}
	if !strings.Contains(statusLine, "200") {
		conn.Close()
		t.Fatalf("unexpected response status: %q", statusLine)
	}
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			conn.Close()
			t.Fatalf("read response headers: %v", readErr)
		}
		if line == "\r\n" {
			break
		}
	}
	if _, err := io.ReadFull(reader, make([]byte, len("ready"))); err != nil {
		conn.Close()
		t.Fatalf("read initial response body: %v", err)
	}
	_ = conn.Close()

	select {
	case events := <-disconnectTimelineResults:
		gotFailure := false
		for _, event := range events {
			if event.Name == "gateway_write_failed" && event.Status == timelineStatusMeasured && event.OffsetMS != nil {
				gotFailure = true
			}
		}
		if !gotFailure {
			t.Fatalf("real TCP disconnect did not surface as a failed ResponseWriter write: %+v", events)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for server-side TCP disconnect handling")
	}
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


type timelineTestForwarder struct {
	err error
}

func (f timelineTestForwarder) Forward(context.Context, core.State, core.ChunkFunc) (core.Result, error) {
	return core.Result{}, f.err
}

func TestRequestTimelineRecordsPipelineFinishOnFailureAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "client_cancelled", err: context.Canceled},
		{name: "upstream_connection_failed", err: errors.New("upstream dial failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			timeline := newRequestTimeline(start)
			ctx := context.WithValue(context.Background(), requestTimelineContextKey{}, timeline)
			if errors.Is(tc.err, context.Canceled) {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelCtx
			}
			pipe := &core.Pipeline{
				Resolve: func(context.Context, core.State) (core.ModelChoice, error) {
					return core.ModelChoice{ProviderID: "test", UpstreamID: "test-model"}, nil
				},
				Forward:  timelineTestForwarder{err: tc.err},
				Observe: inferenceObserver{},
			}
			_, _ = pipe.Run(ctx, core.Request{
				ID: "timeline-" + tc.name, Method: http.MethodPost, Path: "/v1/chat/completions",
				Body: []byte(`{"model":"test-model","messages":[]}`), Started: start,
			})
			var finish *requestTimelineEvent
			for _, event := range timeline.snapshot() {
				if event.Name == "pipeline_finished" {
					copy := event
					finish = &copy
					break
				}
			}
			if finish == nil || finish.Status != timelineStatusMeasured || finish.OffsetMS == nil {
				t.Fatalf("Pipeline Finish must be measured for %s: %+v", tc.name, timeline.snapshot())
			}
		})
	}
}


func TestResponseBodyWriteMissingStatusUsesHTTPBodySemantics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		status int
		want   string
	}{
		{name: "normal_response_header_committed_but_write_failed", method: http.MethodPost, status: http.StatusOK, want: timelineStatusNotReached},
		{name: "head_response", method: http.MethodHead, status: http.StatusMethodNotAllowed, want: timelineStatusNotApplicable},
		{name: "no_content", method: http.MethodPost, status: http.StatusNoContent, want: timelineStatusNotApplicable},
		{name: "reset_content", method: http.MethodPost, status: http.StatusResetContent, want: timelineStatusNotApplicable},
		{name: "not_modified", method: http.MethodPost, status: http.StatusNotModified, want: timelineStatusNotApplicable},
		{name: "informational", method: http.MethodPost, status: http.StatusContinue, want: timelineStatusNotApplicable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := timelineStatusNotReached
			if responseBodyNotApplicable(tc.method, tc.status) {
				got = timelineStatusNotApplicable
			}
			if got != tc.want {
				t.Fatalf("missing response body write status = %q, want %q", got, tc.want)
			}
		})
	}
}


func TestResponseSinkDoesNotRecordSuppressedHeadBody(t *testing.T) {
	timeline := newRequestTimeline(time.Now())
	recorder := httptest.NewRecorder()
	sink := &responseSink{w: recorder, method: http.MethodHead, timeline: timeline}
	if n, err := sink.Write([]byte("suppressed")); err != nil || n != len("suppressed") {
		t.Fatalf("HEAD ResponseWriter semantics changed: n=%d err=%v", n, err)
	}
	if events := timeline.snapshot(); len(events) != 0 {
		t.Fatalf("HEAD body suppression must not be reported as a successful body write: %+v", events)
	}
}
