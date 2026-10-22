package store

import (
	"context"
	"fmt"
	"strings"
)

// InsertStreamEvents 单事务批量插入流式事件，供异步 audit 在请求结束后一次写入。
func (r *Repo) InsertStreamEvents(ctx context.Context, requestID string, events []StreamEvent) error {
	if len(events) == 0 {
		return nil
	}
	const batchRows = 100
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for start := 0; start < len(events); start += batchRows {
		end := start + batchRows
		if end > len(events) {
			end = len(events)
		}
		chunk := events[start:end]
		placeholders := make([]string, 0, len(chunk))
		args := make([]interface{}, 0, len(chunk)*8)
		for _, e := range chunk {
			placeholders = append(placeholders, "(?,?,?,?,?,?,?,?)")
			args = append(args, requestID, e.Seq, e.AtMS, e.Kind, nullIfEmpty(e.Field), e.DeltaLen, nullIfEmpty(e.Preview), nullIfEmpty(e.RawLine))
		}
		q := `INSERT INTO request_stream_events(request_id,seq,at_ms,kind,field_name,delta_len,preview,raw_line) VALUES ` +
			strings.Join(placeholders, ",")
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("batch insert stream events: %w", err)
		}
	}
	return tx.Commit()
}
