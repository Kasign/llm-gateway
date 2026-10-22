package live

import (
	"sync"
	"time"

	"github.com/whl/llm-gateway/internal/store"
)

const maxStreamEvents = 2000
const maxBodyBytes = 8 << 20 // 与 store_payload 流式 buffer 上限一致

// Session 保存进行中请求的完整内存快照。
type Session struct {
	mu              sync.RWMutex
	Log             store.RequestLog
	events          []store.StreamEvent
	reqBody         []byte
	resBody         []byte
	clientHeaders   map[string]string
	upstreamHeaders map[string]string
}

// DetailSnapshot 供管理台详情 API 使用。
type DetailSnapshot struct {
	Log             store.RequestLog
	Events          []store.StreamEvent
	ReqBody         []byte
	ResBody         []byte
	ClientHeaders   map[string]string
	UpstreamHeaders map[string]string
}

func (s *Session) PatchLog(patch store.RequestLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mergeLog(&s.Log, patch)
}

func (s *Session) SetHeaders(client, upstream map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clientHeaders = copyStringMap(client)
	s.upstreamHeaders = copyStringMap(upstream)
}

func (s *Session) SetUpstreamHeaders(upstream map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upstreamHeaders = copyStringMap(upstream)
}

func (s *Session) SetReqBody(b []byte) {
	if len(b) > maxBodyBytes {
		b = b[:maxBodyBytes]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqBody = append([]byte(nil), b...)
}

func (s *Session) SetResBody(b []byte) {
	if len(b) > maxBodyBytes {
		b = b[:maxBodyBytes]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resBody = append([]byte(nil), b...)
	s.Log.ResponseBytes = int64(len(s.resBody))
}

func (s *Session) AppendResBody(chunk []byte, enabled bool) {
	if !enabled || len(chunk) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	remain := maxBodyBytes - len(s.resBody)
	if remain <= 0 {
		return
	}
	if len(chunk) > remain {
		chunk = chunk[:remain]
	}
	s.resBody = append(s.resBody, chunk...)
	s.Log.ResponseBytes = int64(len(s.resBody))
}

func (s *Session) AppendStreamEvent(ev store.StreamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) >= maxStreamEvents {
		return
	}
	s.events = append(s.events, ev)
}

func (s *Session) UpdateStreamScalars(responseBytes int64, ttft *int64, in, out, total *int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if responseBytes > 0 {
		s.Log.ResponseBytes = responseBytes
	}
	if ttft != nil {
		s.Log.TTFTMS = ttft
	}
	if in != nil {
		s.Log.InputTokens = in
	}
	if out != nil {
		s.Log.OutputTokens = out
	}
	if total != nil {
		s.Log.TotalTokens = total
	}
	if !s.Log.StartedAt.IsZero() {
		s.Log.LatencyMS = time.Since(s.Log.StartedAt).Milliseconds()
	}
}

func (s *Session) LogSummary() store.RequestLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.Log
	if out.Status == "" {
		out.Status = "running"
	}
	if !out.StartedAt.IsZero() {
		out.LatencyMS = time.Since(out.StartedAt).Milliseconds()
	}
	return out
}

func (s *Session) SnapshotDetail() DetailSnapshot {
	s.mu.RLock()
	log := s.Log
	events := append([]store.StreamEvent(nil), s.events...)
	reqBody := append([]byte(nil), s.reqBody...)
	resBody := append([]byte(nil), s.resBody...)
	clientHeaders := copyStringMap(s.clientHeaders)
	upstreamHeaders := copyStringMap(s.upstreamHeaders)
	s.mu.RUnlock()
	if log.Status == "" {
		log.Status = "running"
	}
	if !log.StartedAt.IsZero() {
		log.LatencyMS = time.Since(log.StartedAt).Milliseconds()
	}
	return DetailSnapshot{
		Log:             log,
		Events:          events,
		ReqBody:         reqBody,
		ResBody:         resBody,
		ClientHeaders:   clientHeaders,
		UpstreamHeaders: upstreamHeaders,
	}
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (s *Session) StreamEventsCopy() []store.StreamEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]store.StreamEvent(nil), s.events...)
}

func mergeLog(dst *store.RequestLog, patch store.RequestLog) {
	if patch.APIKeyID != 0 {
		dst.APIKeyID = patch.APIKeyID
	}
	if patch.APIKeyName != "" {
		dst.APIKeyName = patch.APIKeyName
	}
	if patch.ModelRequested != "" {
		dst.ModelRequested = patch.ModelRequested
	}
	if patch.ModelActual != "" {
		dst.ModelActual = patch.ModelActual
	}
	dst.Stream = patch.Stream || dst.Stream
	if patch.RequestBytes > 0 {
		dst.RequestBytes = patch.RequestBytes
	}
	if patch.ResponseBytes > 0 {
		dst.ResponseBytes = patch.ResponseBytes
	}
	if patch.HTTPStatus != 0 {
		dst.HTTPStatus = patch.HTTPStatus
	}
	if patch.TTFTMS != nil {
		dst.TTFTMS = patch.TTFTMS
	}
	if patch.InputTokens != nil {
		dst.InputTokens = patch.InputTokens
	}
	if patch.OutputTokens != nil {
		dst.OutputTokens = patch.OutputTokens
	}
	if patch.TotalTokens != nil {
		dst.TotalTokens = patch.TotalTokens
	}
	if patch.ErrorCode != "" {
		dst.ErrorCode = patch.ErrorCode
	}
	if patch.ErrorMessage != "" {
		dst.ErrorMessage = patch.ErrorMessage
	}
	if patch.ClientIP != "" {
		dst.ClientIP = patch.ClientIP
	}
	if patch.ClientName != "" {
		dst.ClientName = patch.ClientName
	}
	if patch.UserAgent != "" {
		dst.UserAgent = patch.UserAgent
	}
	if patch.Endpoint != "" {
		dst.Endpoint = patch.Endpoint
	}
	if patch.RequestURL != "" {
		dst.RequestURL = patch.RequestURL
	}
	if patch.Method != "" {
		dst.Method = patch.Method
	}
}
