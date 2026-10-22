// Package core 是固化的推理主流程：解析、选模型、改写、检查、保活、出网、结束。
// 它不引用鉴权、日志、模型库或 net/http。这些能力由调用方注入的回调提供。
package core

import (
	"context"
	"fmt"
)

// Pipeline 的槽位顺序写死在 Run 里。更换回调不会改变这个顺序。
type Pipeline struct {
	Resolve   Resolver
	Mutate    []Mutator
	Guard     []Guard
	WarmStart WarmFunc
	Forward   Forwarder
	Observe   Observer
}

// Run 执行一次推理往返。Finish 无论成功、失败或 panic 都只发一次。
func (p *Pipeline) Run(ctx context.Context, req Request) (res Result, err error) {
	if p == nil {
		return Result{}, &Error{Status: 500, Code: "internal_error", Message: "pipeline is nil"}
	}
	st := parse(req)
	defer func() {
		if rec := recover(); rec != nil {
			err = &Error{Status: 500, Code: "internal_error", Message: fmt.Sprintf("pipeline panic: %v", rec)}
		}
		if res.Status == 0 && err != nil {
			res.Status, _, _ = Unpack(err)
		}
		res.Err = err
		p.emit(ctx, Event{Stage: StageFinish, State: st, Result: &res, Err: err})
	}()

	p.emit(ctx, Event{Stage: StageParsed, State: st})
	if p.Resolve == nil {
		return Result{}, &Error{Status: 500, Code: "internal_error", Message: "model resolver is nil"}
	}
	model, err := p.Resolve(ctx, st)
	if err != nil {
		return Result{}, err
	}
	st.Model = model
	st.ResolvedAt = timeNow()
	p.emit(ctx, Event{Stage: StageModelResolved, State: st})

	// 先把服务选定的上游 ID 写回 body 的 model，后续改写和上游看到的都是同一个模型。
	st = stampModel(st)
	for _, m := range p.Mutate {
		if m == nil {
			continue
		}
		st, err = m(ctx, st)
		if err != nil {
			return Result{}, err
		}
	}
	st.PreparedAt = timeNow()
	p.emit(ctx, Event{Stage: StagePrepared, State: st})

	// 全局 Guard 先于本次请求附加的 Guard（如 /v1 的 allowed_models）。
	guards := make([]Guard, 0, len(p.Guard)+len(req.Guards))
	guards = append(guards, p.Guard...)
	guards = append(guards, req.Guards...)
	for _, g := range guards {
		if g == nil {
			continue
		}
		if err = g(ctx, st); err != nil {
			return Result{}, err
		}
	}
	st.ReadyAt = timeNow()
	p.emit(ctx, Event{Stage: StageReady, State: st})

	if p.WarmStart != nil {
		st.WarmBytes = p.callWarm(ctx, st)
	}
	st.WarmAt = timeNow()
	p.emit(ctx, Event{Stage: StageWarmStart, State: st})

	if p.Forward == nil {
		return Result{}, &Error{Status: 500, Code: "internal_error", Message: "forwarder is nil"}
	}
	st.UpstreamStart = timeNow()
	p.emit(ctx, Event{Stage: StageUpstreamStart, State: st})
	// chunk 复制一份再发给观察者，Forwarder 可以复用自己的读缓冲区。
	res, err = p.Forward.Forward(ctx, st, func(chunk []byte) {
		p.emit(ctx, Event{Stage: StageChunk, State: st, Chunk: append([]byte(nil), chunk...)})
	})
	return res, err
}

func (p *Pipeline) callWarm(ctx context.Context, st State) (n int) {
	defer func() {
		if recover() != nil {
			n = 0
		}
	}()
	return p.WarmStart(ctx, st)
}

func (p *Pipeline) emit(ctx context.Context, ev Event) {
	p.safeOn(ctx, p.Observe, ev)
	if ev.State.Observe != nil {
		p.safeOn(ctx, ev.State.Observe, ev)
	}
}

func (p *Pipeline) safeOn(ctx context.Context, ob Observer, ev Event) {
	if ob == nil {
		return
	}
	defer func() { _ = recover() }()
	ob.On(ctx, ev)
}
