package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/llama"
	"github.com/whl/llm-gateway/internal/provider"
)

// upstreamPathWithQuery 把调用方原始查询参数附加到出站路径，不污染路由判断所用的 Path。
func upstreamPathWithQuery(path, rawQuery string) string {
	if rawQuery == "" {
		return path
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + rawQuery
}

// inferenceForwarder 是 core Pipeline 唯一的出网点；它解析 Provider 并注入写回、首包与观察回调。
// Provider 负责协议转发；本地模型加载/卸载仍由独立的管理流程完成。
type inferenceForwarder struct{ s *Server }

func (f inferenceForwarder) Forward(ctx context.Context, st core.State, onChunk core.ChunkFunc) (core.Result, error) {
	upPath := st.UpstreamPath
	if upPath == "" {
		upPath = st.Path
	}
	upPath = upstreamPathWithQuery(upPath, st.RawQuery)
	rw, ok := st.Sink.(http.ResponseWriter)
	if !ok || rw == nil {
		return core.Result{}, &core.Error{Status: http.StatusInternalServerError, Code: "internal_error", Message: "response sink is nil"}
	}
	method := st.Method
	if method == "" {
		method = http.MethodPost
	}
	req := &http.Request{Method: method, Header: http.Header(st.Header)}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	logPath := st.Path
	if logPath == "" {
		logPath = strings.SplitN(upPath, "?", 2)[0]
	}
	applog.Printf("[%s] upstream forward: path=%s stream=%v body=%d", st.ID, logPath, st.Stream, len(st.Body))
	ctx = llama.WithOutboundHeaderProbe(ctx, func(h http.Header) {
		f.s.noteActualUpstreamHeaders(st.ID, h)
	})

	if f.s.providers == nil {
		return core.Result{}, &core.Error{Status: http.StatusServiceUnavailable, Code: "provider_not_configured", Message: "inference Provider registry is not configured"}
	}
	active := f.s.providerIDForState(st)
	p, err := f.s.providers.Resolve(active)
	if err != nil {
		return core.Result{}, &core.Error{Status: http.StatusServiceUnavailable, Code: "provider_not_configured", Message: err.Error()}
	}

	var upstreamHeadersMS, firstByteMS *int64
	res, err := p.Forward(ctx, provider.InferenceCall{
		Writer:  rw,
		Request: req,
		Path:    upPath,
		Body:    st.Body,
		ModelID: st.Model.UpstreamID,
		Stream:  st.Stream,
		OnChunk: onChunk,
		OnHeaders: func(_ http.Header, _ int) {
			v := time.Since(st.Started).Milliseconds()
			upstreamHeadersMS = &v
		},
		ClientWriter: func(chunk []byte) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if _, writeErr := rw.Write(chunk); writeErr != nil {
				f.s.failRun(st.ID, "写回客户端失败："+writeErr.Error())
				return writeErr
			}
			return nil
		},
		OnFirstByte: func() {
			v := time.Since(st.Started).Milliseconds()
			firstByteMS = &v
		},
	})
	if st.Stream {
		res.FirstByteMS = firstByteMS
		res.UpstreamHeadersMS = upstreamHeadersMS
	}
	if isCancelErr(err) || isCancelErr(ctx.Err()) {
		if res.Status < 400 {
			res.Status = 499
		}
		return res, context.Canceled
	}
	if mapped := provider.ToCoreError(err); mapped != nil {
		err = mapped
	}
	if res.Status == 0 {
		if err != nil {
			// 保留 Provider 的稳定错误映射，例如上游建连/读取超时应为 504，而不是被默认值覆盖成 502。
			res.Status, _, _ = core.Unpack(err)
		} else {
			res.Status = http.StatusBadGateway
		}
	}
	return res, err
}
