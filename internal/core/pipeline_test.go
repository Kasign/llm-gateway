package core

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type memSink struct {
	hdr    map[string]string
	body   []byte
	status int
	wrote  bool
}

func (m *memSink) SetHeader(k, v string) {
	if m.hdr == nil {
		m.hdr = map[string]string{}
	}
	m.hdr[k] = v
}
func (m *memSink) WriteHeader(status int) {
	if m.wrote {
		return
	}
	m.wrote = true
	m.status = status
}
func (m *memSink) Write(p []byte) (int, error) {
	if !m.wrote {
		m.wrote = true
		if m.status == 0 {
			m.status = 200
		}
	}
	m.body = append(m.body, p...)
	return len(p), nil
}
func (m *memSink) Flush()              {}
func (m *memSink) HeaderWritten() bool { return m.wrote }

type recObs struct {
	stages []Stage
	warm   int
	chunks int
	panic  bool
}

func (o *recObs) On(_ context.Context, ev Event) {
	if o.panic && ev.Stage == StageParsed {
		panic("observer down")
	}
	o.stages = append(o.stages, ev.Stage)
	if ev.Stage == StageWarmStart {
		o.warm = ev.State.WarmBytes
	}
	if ev.Stage == StageChunk {
		o.chunks++
	}
}

func TestRunOrderAndIgnoresClientModel(t *testing.T) {
	var seen string
	obs := &recObs{}
	sink := &memSink{}
	p := &Pipeline{
		Resolve: func(_ context.Context, st State) (ModelChoice, error) {
			if st.ClientModel != "from-client" {
				t.Fatalf("client model = %q", st.ClientModel)
			}
			if st.ServiceModel != "" {
				t.Fatal("service model should be empty")
			}
			return ModelChoice{UpstreamID: "configured"}, nil
		},
		Mutate: []Mutator{func(_ context.Context, st State) (State, error) {
			st.UpstreamPath = st.Path + "/mutated"
			return st, nil
		}},
		Guard: []Guard{func(context.Context, State) error { return nil }},
		WarmStart: func(_ context.Context, st State) int {
			_, _ = st.Sink.Write([]byte(": keepalive\n\n"))
			return len(": keepalive\n\n")
		},
		Forward: forwardFunc(func(_ context.Context, st State, onChunk ChunkFunc) (Result, error) {
			var obj map[string]any
			if err := json.Unmarshal(st.Body, &obj); err != nil {
				t.Fatal(err)
			}
			seen, _ = obj["model"].(string)
			if st.UpstreamPath != "/v1/chat/completions/mutated" {
				t.Fatalf("path %s", st.UpstreamPath)
			}
			onChunk([]byte("data: hi\n\n"))
			ms := int64(40)
			return Result{Status: 200, ResponseBytes: 4, TTFT: &ms}, nil
		}),
		Observe: obs,
	}
	body := []byte(`{"model":"from-client","stream":true}`)
	res, err := p.Run(context.Background(), Request{
		ID: "r1", Method: "POST", Path: "/v1/chat/completions", Body: body, Sink: sink, Started: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != "configured" {
		t.Fatalf("outbound model %q", seen)
	}
	if res.TTFT == nil || *res.TTFT != 40 {
		t.Fatalf("ttft %+v", res.TTFT)
	}
	want := []Stage{StageParsed, StageModelResolved, StagePrepared, StageReady, StageWarmStart, StageUpstreamStart, StageChunk, StageFinish}
	if len(obs.stages) != len(want) {
		t.Fatalf("stages %v", obs.stages)
	}
	for i := range want {
		if obs.stages[i] != want[i] {
			t.Fatalf("stage %d = %s", i, obs.stages[i])
		}
	}
	if obs.warm == 0 || obs.chunks != 1 {
		t.Fatalf("warm %d chunks %d", obs.warm, obs.chunks)
	}
	if string(sink.body) != ": keepalive\n\n" {
		t.Fatalf("sink %q", sink.body)
	}
}

func TestRunUsesServiceModel(t *testing.T) {
	var seen string
	p := &Pipeline{
		Resolve: func(_ context.Context, st State) (ModelChoice, error) {
			if st.ServiceModel == "" {
				t.Fatal("missing service model")
			}
			return ModelChoice{UpstreamID: st.ServiceModel}, nil
		},
		Forward: forwardFunc(func(_ context.Context, st State, _ ChunkFunc) (Result, error) {
			var obj map[string]any
			_ = json.Unmarshal(st.Body, &obj)
			seen, _ = obj["model"].(string)
			return Result{Status: 200}, nil
		}),
	}
	_, err := p.Run(context.Background(), Request{
		Path: "/v1/chat/completions", ServiceModel: "bench-model", Body: []byte(`{"model":"from-client"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != "bench-model" {
		t.Fatalf("got %q", seen)
	}
}

func TestFinishOnceWhenResolveFails(t *testing.T) {
	obs := &recObs{}
	p := &Pipeline{
		Resolve: func(context.Context, State) (ModelChoice, error) {
			return ModelChoice{}, &Error{Status: 503, Code: "no_proxy_model", Message: "未配置推理模型"}
		},
		Forward: forwardFunc(func(context.Context, State, ChunkFunc) (Result, error) {
			t.Fatal("forward should not run")
			return Result{}, nil
		}),
		Observe: obs,
	}
	_, err := p.Run(context.Background(), Request{Body: []byte(`{"model":"x"}`)})
	if err == nil {
		t.Fatal("expected error")
	}
	status, code, _ := Unpack(err)
	if status != 503 || code != "no_proxy_model" {
		t.Fatalf("unpack %d %s", status, code)
	}
	if len(obs.stages) != 2 || obs.stages[0] != StageParsed || obs.stages[1] != StageFinish {
		t.Fatalf("stages %v", obs.stages)
	}
}

func TestObserverPanicDoesNotChangeResult(t *testing.T) {
	obs := &recObs{panic: true}
	p := &Pipeline{
		Resolve: func(context.Context, State) (ModelChoice, error) {
			return ModelChoice{UpstreamID: "m"}, nil
		},
		Forward: forwardFunc(func(context.Context, State, ChunkFunc) (Result, error) {
			return Result{Status: 200, ResponseBytes: 3}, nil
		}),
		Observe: obs,
	}
	res, err := p.Run(context.Background(), Request{Body: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 {
		t.Fatalf("status %d", res.Status)
	}
	var finish int
	for _, s := range obs.stages {
		if s == StageFinish {
			finish++
		}
	}
	if finish != 1 {
		t.Fatalf("finish %d stages %v", finish, obs.stages)
	}
}

func TestWarmBytesAreNotChunksOrTTFT(t *testing.T) {
	obs := &recObs{}
	p := &Pipeline{
		Resolve: func(context.Context, State) (ModelChoice, error) {
			return ModelChoice{UpstreamID: "m"}, nil
		},
		WarmStart: func(context.Context, State) int {
			return 12
		},
		Forward: forwardFunc(func(context.Context, State, ChunkFunc) (Result, error) {
			return Result{Status: 200}, nil
		}),
		Observe: obs,
	}
	res, err := p.Run(context.Background(), Request{Body: []byte(`{"stream":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if res.TTFT != nil {
		t.Fatal("keepalive must not set ttft")
	}
	if obs.warm != 12 || obs.chunks != 0 {
		t.Fatalf("warm %d chunks %d", obs.warm, obs.chunks)
	}
}

func TestGuardCancelFinishesAs499(t *testing.T) {
	var got *Result
	p := &Pipeline{
		Resolve: func(context.Context, State) (ModelChoice, error) {
			return ModelChoice{UpstreamID: "m"}, nil
		},
		Guard: []Guard{func(context.Context, State) error { return context.Canceled }},
		Forward: forwardFunc(func(context.Context, State, ChunkFunc) (Result, error) {
			t.Fatal("forward must not run after guard cancel")
			return Result{}, nil
		}),
		Observe: observerFunc(func(_ context.Context, ev Event) {
			if ev.Stage == StageFinish {
				got = ev.Result
			}
		}),
	}
	_, err := p.Run(context.Background(), Request{Body: []byte(`{}`)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if got == nil || got.Status != 499 {
		t.Fatalf("finish result %+v", got)
	}
}

type observerFunc func(context.Context, Event)

func (f observerFunc) On(ctx context.Context, ev Event) { f(ctx, ev) }

type forwardFunc func(context.Context, State, ChunkFunc) (Result, error)

func (f forwardFunc) Forward(ctx context.Context, st State, onChunk ChunkFunc) (Result, error) {
	return f(ctx, st, onChunk)
}

func TestRequestGuardsRunBeforeGlobalGuards(t *testing.T) {
	var order []string
	forwardCalled := false
	p := &Pipeline{
		Resolve: func(context.Context, State) (ModelChoice, error) {
			return ModelChoice{ModelKey: "private-model", UpstreamID: "private-upstream"}, nil
		},
		Guard: []Guard{func(context.Context, State) error {
			order = append(order, "global")
			return nil
		}},
		Forward: forwardFunc(func(context.Context, State, ChunkFunc) (Result, error) {
			forwardCalled = true
			return Result{Status: 200}, nil
		}),
	}
	_, err := p.Run(context.Background(), Request{
		Body: []byte(`{}`),
		Guards: []Guard{func(context.Context, State) error {
			order = append(order, "request")
			return &Error{Status: 403, Code: "model_not_allowed", Message: "model is not allowed"}
		}},
	})
	if err == nil {
		t.Fatal("expected request-level guard to reject")
	}
	status, code, _ := Unpack(err)
	if status != 403 || code != "model_not_allowed" {
		t.Fatalf("error = %d/%s, want 403/model_not_allowed", status, code)
	}
	if forwardCalled {
		t.Fatal("forwarder must not run after request guard rejection")
	}
	if len(order) != 1 || order[0] != "request" {
		t.Fatalf("guard order = %v, want only request guard", order)
	}
}
