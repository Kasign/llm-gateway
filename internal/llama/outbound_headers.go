package llama

import (
	"context"
	"net/http"
	"strconv"
)

type outboundHeaderProbeKey struct{}

// WithOutboundHeaderProbe 在真正组好发给上游的请求后回调一份 Header。
// 只给推理转发用，探测/下载不会挂这个上下文。
func WithOutboundHeaderProbe(ctx context.Context, fn func(http.Header)) context.Context {
	if ctx == nil || fn == nil {
		return ctx
	}
	return context.WithValue(ctx, outboundHeaderProbeKey{}, fn)
}

func emitOutboundHeaders(ctx context.Context, req *http.Request) {
	if ctx == nil || req == nil {
		return
	}
	fn, _ := ctx.Value(outboundHeaderProbeKey{}).(func(http.Header))
	if fn == nil {
		return
	}
	h := req.Header.Clone()
	if h == nil {
		h = make(http.Header)
	}
	// Host / Content-Length 常常在 Request 字段上，不在 Header map 里。
	if req.Host != "" && h.Get("Host") == "" {
		h.Set("Host", req.Host)
	}
	if req.ContentLength > 0 && h.Get("Content-Length") == "" {
		h.Set("Content-Length", strconv.FormatInt(req.ContentLength, 10))
	}
	fn(h)
}
