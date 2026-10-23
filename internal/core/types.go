package core

import (
	"context"
	"strings"
	"time"
)

// Stage 是主流程已经固定的观察点。新增业务只能订阅这些点，不能再插入阶段。
type Stage string

const (
	StageParsed        Stage = "parsed"
	StageModelResolved Stage = "model_resolved"
	StagePrepared      Stage = "prepared"
	StageReady         Stage = "ready"
	StageWarmStart     Stage = "warm_start"
	StageUpstreamStart Stage = "upstream_start"
	StageChunk         Stage = "chunk"
	StageFinish        Stage = "finish"
)

// ResponseSink 是写回调用方的最小出口。core 不依赖 net/http。
type ResponseSink interface {
	SetHeader(key, value string)
	WriteHeader(status int)
	Write(p []byte) (int, error)
	Flush()
	HeaderWritten() bool
}

// Request 是一次推理的入参。ServiceModel 只给进程内调用方，外部 HTTP 必须留空。
type Request struct {
	ID           string
	Method       string
	Path         string
	RawQuery     string // 原始查询字符串单独保存，避免影响路由匹配与协议判断。
	Header       map[string][]string
	Body         []byte
	ServiceModel       string
	ServiceProviderID  string // 网关内部调用可固定 Provider；外部 HTTP 请求不会设置此字段。
	Sink               ResponseSink
	Started      time.Time
	ClientIP     string
	ClientName   string
	UserAgent    string
	RequestURL   string // 调用方访问的完整 URL（协议 + Host + 路径 + 查询）。进程内调用留空。
	Guards       []Guard
	Observe      Observer
}

// HeaderGet 按大小写不敏感的方式读取一个请求头。
func (r Request) HeaderGet(key string) string {
	for k, vs := range r.Header {
		if len(vs) == 0 {
			continue
		}
		if strings.EqualFold(k, key) {
			return vs[0]
		}
	}
	return ""
}

// ModelChoice 是服务选定的模型，不是客户端 JSON 里的 model。
type ModelChoice struct {
	ProviderID string // Resolver 选定的 Provider；后续阶段必须沿用。
	Hint       string
	ModelKey   string
	UpstreamID string
}

// State 是 Run 在各槽位之间传递的请求状态。
type State struct {
	Request
	ClientModel   string
	Stream        bool
	Model         ModelChoice
	UpstreamPath  string
	OriginalBody  []byte
	WarmBytes     int
	ParsedAt      time.Time
	ResolvedAt    time.Time
	PreparedAt    time.Time
	ReadyAt       time.Time
	WarmAt        time.Time
	UpstreamStart time.Time
}

// Result 是出网结果。TTFT 只由真实上游数据产生，保活字节不算。
type Result struct {
	Status        int
	ResponseBytes int64
	Body          []byte
	TTFT             *int64 // 首个实际输出（reasoning/content），不是 role 元数据或首字节。
	FirstByteMS      *int64
	UpstreamHeadersMS *int64
	Err              error
}

// Event 是发给观察者的一次通知。Chunk 只携带上游字节，不含保活。
type Event struct {
	Stage  Stage
	State  State
	Chunk  []byte
	Result *Result
	Err    error
}

// Resolver 决定真正使用的模型。
type Resolver func(ctx context.Context, st State) (ModelChoice, error)

// Mutator 按注册顺序改写出站的路径和 body。
type Mutator func(ctx context.Context, st State) (State, error)

// Guard 在出网前检查。返回错误则请求失败。
type Guard func(ctx context.Context, st State) error

// WarmFunc 决定是否向调用方写保活。返回写入字节数；失败由实现自己记录。
type WarmFunc func(ctx context.Context, st State) int

// ChunkFunc 接收上游的一块真实响应。
type ChunkFunc func(chunk []byte)

// Forwarder 是唯一出网点。
type Forwarder interface {
	Forward(ctx context.Context, st State, onChunk ChunkFunc) (Result, error)
}

// Observer 只记录，不能改变主流程的结果。
type Observer interface {
	On(ctx context.Context, ev Event)
}
