package server

import (
	"context"
	"sync"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
	"github.com/whl/llm-gateway/internal/auth"
	"github.com/whl/llm-gateway/internal/logutil"
)

// callState 保存一次 Run 期间各回调共享、但不该写进主流程 State 的数据：
// 门外鉴权得到的 Key、Resolve 得到的完整模型记录、日志观察者累计的响应与 token。
// 键是请求 ID，由调用方在 Run 前 callBegin、Run 后 callDrop。
type callState struct {
	mu              sync.Mutex
	key             *auth.Key
	target          proxyModelTarget
	trace           *requestTrace
	resp            []byte // 仅 store_payload 开启时累积，最多 8MB
	tokens          logutil.TokenHarvest
	lastLive        time.Time
	respBytes       int64
	cancel          context.CancelFunc
	cancelReason    string
	keepaliveStop   func()
	clientHeaders   map[string]string
	upstreamHeaders map[string]string
	tokenCarry      string // 流式 usage 半行，拼到下一包再解析
}

func (s *Server) callBegin(id string, key *auth.Key) *callState {
	cs := &callState{key: key}
	s.calls.Store(id, cs)
	return cs
}

func (s *Server) callGet(id string) *callState {
	v, ok := s.calls.Load(id)
	if !ok {
		return nil
	}
	cs, _ := v.(*callState)
	return cs
}

func (s *Server) callDrop(id string) {
	s.calls.Delete(id)
}

// noteCancelReason 记下第一次结束原因。之后的取消不再覆盖。
func (s *Server) noteCancelReason(id, reason string) {
	if reason == "" {
		return
	}
	cs := s.callGet(id)
	if cs == nil {
		return
	}
	cs.mu.Lock()
	if cs.cancelReason == "" {
		cs.cancelReason = reason
	}
	cs.mu.Unlock()
}

func (s *Server) cancelReason(id string) string {
	cs := s.callGet(id)
	if cs == nil {
		return ""
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.cancelReason
}

// failRun 记录结束原因并取消这次推理。原因已存在时保留先写入的那条。
func (s *Server) failRun(id, reason string) {
	s.noteCancelReason(id, reason)
	if reason != "" {
		applog.Printf("[%s] inference stop: %s", id, reason)
	}
	s.stopRun(id)
}

func (s *Server) setCallCancel(id string, cancel context.CancelFunc) {
	cs := s.callGet(id)
	if cs == nil {
		return
	}
	cs.mu.Lock()
	cs.cancel = cancel
	cs.mu.Unlock()
}

func (s *Server) setKeepaliveStop(id string, stop func()) {
	cs := s.callGet(id)
	if cs == nil {
		return
	}
	cs.mu.Lock()
	cs.keepaliveStop = stop
	cs.mu.Unlock()
}

// stopKeepalive 通知保活轮询退出。不等待当前这次写出：Write 可能因客户端不读而堵住，
// 若在这里等待，请求结束和调用记录入库都会被拖住。已经写成功的保活在写出时就记入了事件。
func (s *Server) stopKeepalive(id string) {
	cs := s.callGet(id)
	if cs == nil {
		return
	}
	cs.mu.Lock()
	stop := cs.keepaliveStop
	cs.keepaliveStop = nil
	cs.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// cancelCall 取消挂在这次调用上的 context。runs 里没有记录时还能从这里停掉正在进行的 Run。
func (s *Server) cancelCall(id string) bool {
	cs := s.callGet(id)
	if cs == nil {
		return false
	}
	cs.mu.Lock()
	fn := cs.cancel
	cs.cancel = nil
	cs.mu.Unlock()
	if fn == nil {
		return false
	}
	fn()
	return true
}
