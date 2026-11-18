package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/provider"
	"github.com/whl/llm-gateway/internal/tools/coordinator"
)

var (
	errBufferedResponseTooLarge = errors.New("buffered Provider response exceeds the configured size limit")
	errBufferedResponseMismatch = errors.New("Provider response body differs from the buffered writer body")
	errBufferedStatusMismatch   = errors.New("Provider response status differs from the buffered writer status")
	errBufferedProviderPanic    = errors.New("Provider panicked during buffered tool inference")
)

// bufferedResponseWriter 捕获 Provider 的非流式响应，避免模型中间轮次提交给真实客户端。
type bufferedResponseWriter struct {
	header  http.Header
	status  int
	body    bytes.Buffer
	maxBody int
	err     error
}

func newBufferedResponseWriter(maxBody int) *bufferedResponseWriter {
	if maxBody <= 0 {
		maxBody = coordinator.MaxModelResponseBytes
	}
	return &bufferedResponseWriter{
		header: make(http.Header),
		maxBody: maxBody,
	}
}

func (w *bufferedResponseWriter) Header() http.Header {
	if w == nil {
		return nil
	}
	return w.header
}

func (w *bufferedResponseWriter) WriteHeader(status int) {
	if w == nil || w.status != 0 {
		return
	}
	w.status = status
}

func (w *bufferedResponseWriter) HeaderWritten() bool {
	return w != nil && w.status != 0
}

func (w *bufferedResponseWriter) Write(body []byte) (int, error) {
	if w == nil {
		return 0, errors.New("buffered response writer is nil")
	}
	if w.err != nil {
		return 0, w.err
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if len(body) > w.maxBody-w.body.Len() {
		w.err = errBufferedResponseTooLarge
		return 0, w.err
	}
	return w.body.Write(body)
}

func (w *bufferedResponseWriter) bodyBytes() []byte {
	if w == nil {
		return nil
	}
	return append([]byte(nil), w.body.Bytes()...)
}

type bufferedModelCaller struct {
	onRoundComplete func(time.Duration)
	requestID   string
	providerID  string
	provider    provider.Provider
	request     *http.Request
	path        string
	modelID     string
	onDispatch  func()
	onHeaders   func(http.Header, int)
	maxBody     int
}

func (c *bufferedModelCaller) Complete(ctx context.Context, requestBody []byte) (response coordinator.ModelResponse, err error) {
	if c != nil && c.onRoundComplete != nil {
		started := time.Now()
		defer func() { func() { defer func() { _ = recover() }(); c.onRoundComplete(time.Since(started)) }() }()
	}
	if c == nil || c.provider == nil || c.request == nil || len(requestBody) == 0 {
		return coordinator.ModelResponse{}, errors.New("buffered ModelCaller is unavailable")
	}
	if ctx == nil {
		return coordinator.ModelResponse{}, errors.New("buffered ModelCaller context is nil")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			providerID := c.providerID
			if providerID == "" {
				providerID = "unknown"
			}
			applog.Printf("[%s] buffered Provider panic: provider=%s panic_type=%T\n%s", c.requestID, providerID, recovered, debug.Stack())
			response = coordinator.ModelResponse{}
			err = errBufferedProviderPanic
		}
	}()

	writer := newBufferedResponseWriter(c.maxBody)
	req := c.request.Clone(ctx)
	req.Header = c.request.Header.Clone()
	result, forwardErr := c.provider.Forward(ctx, provider.InferenceCall{
		Writer: writer,
		Request: req,
		Path: c.path,
		Body: append([]byte(nil), requestBody...),
		ModelID: c.modelID,
		Stream: false,
		OnUpstreamDispatch: c.onDispatch,
		OnHeaders: c.onHeaders,
	})
	if writer.err != nil {
		return coordinator.ModelResponse{}, writer.err
	}
	status := result.Status
	if status == 0 {
		status = writer.status
	} else if writer.status != 0 && writer.status != status {
		return coordinator.ModelResponse{}, errBufferedStatusMismatch
	}
	body := append([]byte(nil), result.Body...)
	writtenBody := writer.bodyBytes()
	if len(body) == 0 {
		body = writtenBody
	} else if len(writtenBody) > 0 && !bytes.Equal(body, writtenBody) {
		return coordinator.ModelResponse{}, errBufferedResponseMismatch
	}
	maxBody := c.maxBody
	if maxBody <= 0 {
		maxBody = coordinator.MaxModelResponseBytes
	}
	if len(body) > maxBody {
		return coordinator.ModelResponse{}, errBufferedResponseTooLarge
	}
	if status == 0 && len(body) > 0 {
		status = http.StatusOK
	}
	upstreamStatus := result.UpstreamStatus
	if upstreamStatus == 0 {
		upstreamStatus = status
	}
	response = coordinator.ModelResponse{
		StatusCode: status,
		UpstreamStatus: upstreamStatus,
		Header: writer.Header().Clone(),
		Body: body,
	}
	return response, forwardErr
}

var _ http.ResponseWriter = (*bufferedResponseWriter)(nil)
var _ interface{ HeaderWritten() bool } = (*bufferedResponseWriter)(nil)
var _ interface {
	Complete(context.Context, []byte) (coordinator.ModelResponse, error)
} = (*bufferedModelCaller)(nil)

