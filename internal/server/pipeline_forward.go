package server

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/llama"
)

// inferenceForwarder 是主流程唯一的出网实现，包住 llama.Client 的 Proxy / StreamProxy。
// 它只负责转发和写回，不写请求日志；每块上游数据通过 onChunk 交给观察者。
// 上游字节原样写回客户端。写回失败时结束这次推理。
type inferenceForwarder struct{ s *Server }

func (f inferenceForwarder) Forward(ctx context.Context, st core.State, onChunk core.ChunkFunc) (core.Result, error) {
	upPath := st.UpstreamPath
	if upPath == "" {
		upPath = st.Path
	}
	// llama.Client 需要 http.ResponseWriter。responseSink（/v1）和 memSink（benchmark）都实现了它。
	rw, ok := st.Sink.(http.ResponseWriter)
	if !ok || rw == nil {
		return core.Result{}, &core.Error{Status: 500, Code: "internal_error", Message: "response sink is nil"}
	}
	method := st.Method
	if method == "" {
		method = http.MethodPost
	}
	// 只用于携带方法和请求头；copyRequestHeaders 会去掉 Authorization / X-API-Key，网关 Key 不会发给上游。
	req := &http.Request{Method: method, Header: http.Header(st.Header)}
	if req.Header == nil {
		req.Header = http.Header{}
	}
	applog.Printf("[%s] upstream forward: path=%s stream=%v body=%d", st.ID, upPath, st.Stream, len(st.Body))
	// 出网组好请求后记下实际上游头。不改 Pipeline.Run。
	ctx = llama.WithOutboundHeaderProbe(ctx, func(h http.Header) {
		f.s.noteActualUpstreamHeaders(st.ID, h)
	})
	if st.Stream {
		return f.stream(ctx, st, req, rw, upPath, onChunk)
	}
	n, body, status, err := f.s.llamaClient().Proxy(ctx, rw, req, upPath, bytes.NewReader(st.Body))
	if isCancelErr(err) || isCancelErr(ctx.Err()) {
		if status < 400 {
			status = 499
		}
		return core.Result{Status: status, ResponseBytes: n, Body: body}, context.Canceled
	}
	if status == 0 {
		status = http.StatusBadGateway
	}
	return core.Result{Status: status, ResponseBytes: n, Body: body}, err
}

func (f inferenceForwarder) stream(ctx context.Context, st core.State, req *http.Request, w http.ResponseWriter, upPath string, onChunk core.ChunkFunc) (core.Result, error) {
	var bytesN int64
	var status int
	var ttft *int64
	err := f.s.llamaClient().StreamProxy(ctx, w, req, upPath, bytes.NewReader(st.Body), func(chunk []byte) {
		bytesN += int64(len(chunk))
		// StreamProxy 已先 Flush 当前块。在进入同步观察器之前采样，避免日志处理耗时污染 TTFT。
		if ttft == nil {
			v := time.Since(st.Started).Milliseconds()
			ttft = &v
		}
		if onChunk != nil {
			onChunk(chunk)
		}
	}, func(_ http.Header, code int) {
		status = code
	}, func(p []byte) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_, werr := w.Write(p)
		if werr != nil {
			f.s.failRun(st.ID, "写回客户端失败："+werr.Error())
			return werr
		}
		return nil
	})
	res := core.Result{Status: status, ResponseBytes: bytesN, TTFT: ttft}
	if isCancelErr(err) || isCancelErr(ctx.Err()) {
		if res.Status < 400 {
			res.Status = 499
		}
		return res, context.Canceled
	}
	if res.Status == 0 {
		res.Status = http.StatusBadGateway
	}
	return res, err
}
