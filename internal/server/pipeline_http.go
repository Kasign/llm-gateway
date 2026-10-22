package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/auth"
	"github.com/whl/llm-gateway/internal/core"
	"github.com/whl/llm-gateway/internal/store"
)

const maxInferenceRequestBodyBytes int64 = 64 << 20

// readLimitedRequestBody 严格限制请求体大小。LimitReader 在超限后会表现为 EOF，
// MaxBytesReader 则会返回 *http.MaxBytesError，让调用方可以明确拒绝过大的请求。
func readLimitedRequestBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
}

// serveInference 是 /v1 推理的门外：鉴权、限流、读 body，然后进入固化的 Run。
// 鉴权和限流不属于主流程，放在读 body 之前，非法请求不消耗内存也不进 Run。
// 门外拒绝的请求仍写一条请求日志（rejectInference），方便在调用记录里排查。
func (s *Server) serveInference(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id := requestID(r.Context())
	if r.Method != http.MethodPost {
		s.rejectInference(w, r, id, start, nil, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	key, code := s.authenticateOutcome(r.Context(), r)
	if code != "" {
		msg := "Invalid API key"
		status := http.StatusUnauthorized
		if code == "key_expired" {
			msg = "API key expired"
		}
		applog.Printf("[%s] auth failed: code=%s client=%s", id, code, clientIP(r, s.trustedProxyRanges))
		s.rejectInference(w, r, id, start, key, status, code, msg)
		return
	}
	allowed, _, err := s.limiter.Allow(r.Context(), strconv.FormatUint(key.ID, 10), key.RPM)
	if err != nil {
		applog.Printf("[%s] rate limiter unavailable: %v", id, err)
		s.rejectInference(w, r, id, start, key, http.StatusServiceUnavailable, "rate_limit_unavailable", err.Error())
		return
	}
	if !allowed {
		applog.Printf("[%s] rate limit exceeded: key_id=%d", id, key.ID)
		s.rejectInference(w, r, id, start, key, http.StatusTooManyRequests, "rate_limit_exceeded", "rate limit exceeded")
		return
	}
	body, err := readLimitedRequestBody(w, r, maxInferenceRequestBodyBytes)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.rejectInference(w, r, id, start, key, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds the 64 MiB limit")
		} else {
			s.rejectInference(w, r, id, start, key, http.StatusBadRequest, "invalid_body", err.Error())
		}
		return
	}
	// 跟随客户端连接，并受网关超时限制。取消函数同时交给管理端「结束」。
	ctx, cancel := s.proxyRequestContext(r.Context())
	defer cancel()
	s.runs.Register(id, cancel)
	defer s.runs.Unregister(id)
	s.callBegin(id, key)
	s.setCallCancel(id, cancel)
	defer s.callDrop(id)

	sink := &responseSink{w: w}
	_, err = s.pipe.Run(ctx, core.Request{
		ID: id, Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Header: headerMap(r.Header), Body: body,
		Sink: sink, Started: start, ClientIP: clientIP(r, s.trustedProxyRanges), ClientName: requestClientName(r),
		UserAgent: r.UserAgent(), RequestURL: requestPublicURL(r), Guards: []core.Guard{s.allowModelsGuard(key)},
	})
	// 已经写过响应头（保活或上游数据）时不能再补 JSON 错误，否则会和流内容混在一起。
	if err != nil && !sink.HeaderWritten() {
		status, ecode, msg := core.Unpack(err)
		jsonError(w, status, ecode, msg)
	}
}

func (s *Server) rejectInference(w http.ResponseWriter, r *http.Request, id string, start time.Time, key *auth.Key, status int, code, msg string) {
	s.markCallRecorded(r.Context())
	end := time.Now()
	item := store.RequestLog{
		RequestID: id, ClientIP: clientIP(r, s.trustedProxyRanges), ClientName: requestClientName(r), UserAgent: r.UserAgent(),
		Endpoint: r.URL.Path, RequestURL: requestPublicURL(r), Method: r.Method, Status: "error", HTTPStatus: status, StartedAt: start, FinishedAt: &end,
		LatencyMS: end.Sub(start).Milliseconds(), ErrorCode: code, ErrorMessage: msg,
	}
	if key != nil {
		item.APIKeyID = key.ID
		item.APIKeyName = key.Name
	}
	if s.audit != nil {
		s.audit.CreateRequest(item)
		s.audit.FinishRequest(item)
	}
	jsonError(w, status, code, msg)
}

func headerMap(h http.Header) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, vs := range h {
		cp := append([]string(nil), vs...)
		out[k] = cp
	}
	return out
}

func (s *Server) cancelRequestAPI(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	id = normalizeRequestID(id)
	if id == "" {
		jsonError(w, http.StatusBadRequest, "invalid_request", "request id required")
		return
	}
	// 进程内的 Run 优先取消。库里残留的 running（例如进程重启后没写完）没有 cancel 函数，
	// 也要把记录收成 cancelled，否则列表会一直显示运行中。
	stopped := s.stopRun(id)
	closed := s.closeRunningRequest(r.Context(), id)
	if !stopped && !closed {
		jsonError(w, http.StatusConflict, "not_running", "这条调用已经不在运行")
		return
	}
	applog.Printf("[%s] inference cancel requested stopped=%v closed=%v", id, stopped, closed)
	writeJSON(w, map[string]any{"request_id": id, "cancelled": true})
}

func normalizeRequestID(id string) string {
	id = strings.TrimSpace(id)
	if decoded, err := url.PathUnescape(id); err == nil {
		id = decoded
	}
	return strings.Trim(strings.TrimSpace(id), "/")
}

func (s *Server) stopRun(id string) bool {
	s.noteCancelReason(id, "管理端结束这次调用")
	stopped := false
	if s.runs != nil && s.runs.Cancel(id) {
		stopped = true
	}
	if s.cancelCall(id) {
		stopped = true
	}
	return stopped
}

// closeRunningRequest 把仍显示为运行中的记录收口，并移出实时看板。
// 列表合并时只要看板里还在，就会继续显示运行中。
func (s *Server) closeRunningRequest(ctx context.Context, id string) bool {
	sess := s.liveSession(id)
	liveHit := sess != nil
	var started time.Time
	if liveHit {
		started = sess.LogSummary().StartedAt
	}
	if s.live != nil {
		s.live.Unregister(id)
	}
	changed := false
	if s.logs != nil {
		if started.IsZero() {
			if l, err := s.logs.Get(ctx, id); err == nil && l != nil {
				started = l.StartedAt
			}
		}
		end := time.Now()
		latency := int64(0)
		if !started.IsZero() {
			latency = end.Sub(started).Milliseconds()
		}
		msg := s.cancelReason(id)
		if msg == "" {
			msg = "管理端结束这次调用"
		}
		ok, err := s.logs.MarkCancelled(ctx, id, end, latency, msg)
		if err != nil {
			applog.Printf("[%s] mark cancelled: %v", id, err)
		}
		changed = ok
	}
	if liveHit || changed {
		s.invalidateRequestListCache()
	}
	return liveHit || changed
}
