// store 持久化每次 API 调用的摘要与可选的请求/响应正文、流式事件，供管理台查询与排障。
package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// RequestLog 是一条进入网关的调用记录：谁、从哪个 URL、何时、什么模型、耗时与 token 等。
type RequestLog struct {
	RequestID      string     `json:"request_id"`
	APIKeyID       uint64     `json:"api_key_id"`
	APIKeyName     string     `json:"api_key_name"`
	ClientIP       string     `json:"client_ip"`
	ClientName     string     `json:"client_name,omitempty"`
	UserAgent      string     `json:"user_agent,omitempty"`
	Endpoint       string     `json:"endpoint"`
	RequestURL     string     `json:"request_url,omitempty"`
	Method         string     `json:"method"`
	ModelRequested string     `json:"model_requested,omitempty"`
	ModelActual    string     `json:"model_actual,omitempty"`
	Stream         bool       `json:"stream"`
	Status         string     `json:"status"`
	HTTPStatus         int        `json:"http_status"` // Gateway 实际提交给调用方的状态码。
	UpstreamHTTPStatus int        `json:"upstream_http_status,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	LatencyMS      int64      `json:"latency_ms"`
	TTFTMS         *int64     `json:"ttft_ms,omitempty"`
	InputTokens    *int64     `json:"input_tokens,omitempty"`
	OutputTokens   *int64     `json:"output_tokens,omitempty"`
	TotalTokens    *int64     `json:"total_tokens,omitempty"`
	RequestBytes   int64      `json:"request_bytes"`
	ResponseBytes  int64      `json:"response_bytes"`
	ErrorCode      string     `json:"error_code,omitempty"`
	ErrorMessage   string     `json:"error_message,omitempty"`
	AnalysisJSON   string     `json:"analysis_json,omitempty"`
}

// StreamEvent 记录流式响应中的单个 SSE 片段，便于回放「首 token 时间」等细节。
type StreamEvent struct {
	Seq      int    `json:"seq"`
	AtMS     int64  `json:"at_ms"`
	Kind     string `json:"kind"`
	Field    string `json:"field,omitempty"`
	DeltaLen int    `json:"delta_len,omitempty"`
	Preview  string `json:"preview,omitempty"`
	RawLine  string `json:"raw_line,omitempty"`
}

// Repo 访问 request_logs、request_payloads、request_stream_events 等表。
type Repo struct{ db *sql.DB }

func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

func (r *Repo) InsertStreamEvent(ctx context.Context, requestID string, e StreamEvent) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO request_stream_events(request_id,seq,at_ms,kind,field_name,delta_len,preview,raw_line) VALUES(?,?,?,?,?,?,?,?)`,
		requestID, e.Seq, e.AtMS, e.Kind, nullIfEmpty(e.Field), e.DeltaLen, nullIfEmpty(e.Preview), nullIfEmpty(e.RawLine))
	return err
}

func (r *Repo) ListStreamEvents(ctx context.Context, requestID string, limit int) ([]StreamEvent, error) {
	if limit <= 0 || limit > maxStreamEventsPerRequest {
		limit = 500
	}
	rows, err := r.db.QueryContext(ctx, `SELECT seq,at_ms,kind,COALESCE(field_name,''),COALESCE(delta_len,0),COALESCE(preview,''),COALESCE(raw_line,'') FROM request_stream_events WHERE request_id=? ORDER BY seq ASC LIMIT ?`, requestID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StreamEvent
	for rows.Next() {
		var e StreamEvent
		if err := rows.Scan(&e.Seq, &e.AtMS, &e.Kind, &e.Field, &e.DeltaLen, &e.Preview, &e.RawLine); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

const maxStreamEventsPerRequest = 2000

func (r *Repo) CreateRequest(ctx context.Context, l RequestLog) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO request_logs(request_id,api_key_id,api_key_name,client_ip,client_name,user_agent,endpoint,request_url,method,model_requested,model_actual,stream,status,started_at,request_bytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, l.RequestID, l.APIKeyID, l.APIKeyName, l.ClientIP, l.ClientName, l.UserAgent, l.Endpoint, nullIfEmpty(l.RequestURL), l.Method, l.ModelRequested, l.ModelActual, l.Stream, l.Status, l.StartedAt, l.RequestBytes)
	return err
}

func (r *Repo) UpdateRequestBytes(ctx context.Context, requestID string, n int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE request_logs SET request_bytes=? WHERE request_id=?`, n, requestID)
	return err
}

func (r *Repo) PatchRequestAfterParse(ctx context.Context, requestID, modelRequested string, stream bool) error {
	_, err := r.db.ExecContext(ctx, `UPDATE request_logs SET stream=?,model_requested=COALESCE(NULLIF(?,''),model_requested) WHERE request_id=?`, stream, modelRequested, requestID)
	return err
}

// PatchRequestModelActual 写入网关实际发给上游的 model，不改客户端传入的 model_requested。
func (r *Repo) PatchRequestModelActual(ctx context.Context, requestID, modelActual string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE request_logs SET model_actual=COALESCE(NULLIF(?,''),model_actual) WHERE request_id=?`, modelActual, requestID)
	return err
}

// MarkCancelled 只把仍为 running 的记录改成 cancelled，不改 stream 和 token。
// 返回是否更新到一行。已经结束的记录不会被覆盖。
func (r *Repo) MarkCancelled(ctx context.Context, id string, finished time.Time, latencyMS int64, message string) (bool, error) {
	if strings.TrimSpace(message) == "" {
		message = "request cancelled"
	}
	res, err := r.db.ExecContext(ctx, `UPDATE request_logs SET status='cancelled', http_status=499, finished_at=?, latency_ms=?, error_code='cancelled', error_message=? WHERE request_id=? AND status='running'`, finished, latencyMS, message, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// FinishRequest 写入请求最终指标。管理员取消可能并发落库；一旦 status 已变成 cancelled，
 // 本次 Finish 只补充 token/响应等诊断字段，不得把 cancelled 覆盖为 success/error。
func (r *Repo) FinishRequest(ctx context.Context, l RequestLog) error {
	_, err := r.db.ExecContext(ctx, `UPDATE request_logs SET
		api_key_id=COALESCE(NULLIF(?,0),api_key_id),
		api_key_name=COALESCE(NULLIF(?,''),api_key_name),
		model_requested=COALESCE(NULLIF(?,''),model_requested),
		model_actual=COALESCE(NULLIF(?,''),model_actual),
		request_url=COALESCE(NULLIF(?,''),request_url),
		stream=?,
		http_status=CASE WHEN status='cancelled' THEN http_status ELSE ? END,
		upstream_http_status=COALESCE(NULLIF(?,0),upstream_http_status),
		finished_at=CASE WHEN status='cancelled' THEN finished_at ELSE ? END,
		latency_ms=CASE WHEN status='cancelled' THEN latency_ms ELSE ? END,
		ttft_ms=?,input_tokens=?,output_tokens=?,total_tokens=?,response_bytes=?,
		error_code=CASE WHEN status='cancelled' THEN error_code ELSE ? END,
		error_message=CASE WHEN status='cancelled' THEN error_message ELSE ? END,
		status=CASE WHEN status='cancelled' THEN status ELSE ? END
		WHERE request_id=?`,
		l.APIKeyID, l.APIKeyName, l.ModelRequested, l.ModelActual, l.RequestURL, l.Stream,
		l.HTTPStatus, l.UpstreamHTTPStatus, l.FinishedAt, l.LatencyMS, l.TTFTMS,
		l.InputTokens, l.OutputTokens, l.TotalTokens, l.ResponseBytes,
		l.ErrorCode, l.ErrorMessage, l.Status, l.RequestID)
	return err
}

func (r *Repo) SaveRequestAnalysis(ctx context.Context, requestID, analysisJSON string) error {
	if strings.TrimSpace(analysisJSON) == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `UPDATE request_logs SET analysis_json=? WHERE request_id=?`, analysisJSON, requestID)
	return err
}

func (r *Repo) GetRequestAnalysis(ctx context.Context, requestID string) (string, error) {
	var raw sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT analysis_json FROM request_logs WHERE request_id=?`, requestID).Scan(&raw)
	if err != nil {
		return "", err
	}
	if raw.Valid {
		return raw.String, nil
	}
	return "", nil
}
func (r *Repo) SavePayload(ctx context.Context, requestID string, reqBody, resBody []byte) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO request_payloads(request_id,request_body,response_body,created_at) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE request_body=VALUES(request_body),response_body=VALUES(response_body)`, requestID, string(reqBody), string(resBody), time.Now())
	return err
}

// SavePayloadHeaders 只写打码后的请求头。与 body 分开 upsert，避免互相覆盖。
func (r *Repo) SavePayloadHeaders(ctx context.Context, requestID, clientJSON, upstreamJSON string) error {
	if requestID == "" || (clientJSON == "" && upstreamJSON == "") {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO request_payloads(request_id,request_headers_json,upstream_headers_json,created_at) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE request_headers_json=VALUES(request_headers_json),upstream_headers_json=VALUES(upstream_headers_json)`, requestID, nullIfEmpty(clientJSON), nullIfEmpty(upstreamJSON), time.Now())
	return err
}

func (r *Repo) GetPayload(ctx context.Context, requestID string) (map[string]string, error) {
	row := r.db.QueryRowContext(ctx, `SELECT COALESCE(request_body,''),COALESCE(response_body,''),COALESCE(request_headers_json,''),COALESCE(upstream_headers_json,'') FROM request_payloads WHERE request_id=?`, requestID)
	var a, b, c, d string
	if err := row.Scan(&a, &b, &c, &d); err != nil {
		return nil, err
	}
	return map[string]string{"request_body": a, "response_body": b, "request_headers": c, "upstream_headers": d}, nil
}
func (r *Repo) Get(ctx context.Context, id string) (*RequestLog, error) {
	rows, err := r.Query(ctx, 1, id, "", "", "")
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return &rows[0], nil
}
func (r *Repo) Query(ctx context.Context, limit int, requestID, modelName, status, keyName string) ([]RequestLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	q := `SELECT request_id,COALESCE(api_key_id,0),COALESCE(api_key_name,''),COALESCE(client_ip,''),COALESCE(client_name,''),COALESCE(user_agent,''),endpoint,COALESCE(request_url,''),method,COALESCE(model_requested,''),COALESCE(model_actual,''),stream,status,COALESCE(http_status,0),COALESCE(upstream_http_status,0),started_at,finished_at,COALESCE(latency_ms,0),ttft_ms,input_tokens,output_tokens,total_tokens,COALESCE(request_bytes,0),COALESCE(response_bytes,0),COALESCE(error_code,''),COALESCE(error_message,'') FROM request_logs WHERE 1=1`
	var args []interface{}
	if requestID != "" {
		q += " AND request_id=?"
		args = append(args, requestID)
	}
	if modelName != "" {
		q += " AND (model_requested LIKE ? OR model_actual LIKE ?)"
		like := "%" + modelName + "%"
		args = append(args, like, like)
	}
	if status != "" {
		q += " AND status=?"
		args = append(args, status)
	}
	if keyName != "" {
		q += " AND api_key_name LIKE ?"
		args = append(args, "%"+keyName+"%")
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RequestLog
	for rows.Next() {
		var l RequestLog
		if err := rows.Scan(&l.RequestID, &l.APIKeyID, &l.APIKeyName, &l.ClientIP, &l.ClientName, &l.UserAgent, &l.Endpoint, &l.RequestURL, &l.Method, &l.ModelRequested, &l.ModelActual, &l.Stream, &l.Status, &l.HTTPStatus, &l.UpstreamHTTPStatus, &l.StartedAt, &l.FinishedAt, &l.LatencyMS, &l.TTFTMS, &l.InputTokens, &l.OutputTokens, &l.TotalTokens, &l.RequestBytes, &l.ResponseBytes, &l.ErrorCode, &l.ErrorMessage); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ClearRequests removes all request logs and payloads. Returns deleted log count.
func (r *Repo) ClearRequests(ctx context.Context) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM request_payloads`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM request_stream_events`); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM request_logs`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}
