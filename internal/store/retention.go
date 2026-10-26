package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const maxRequestRetentionBatchSize = 5000

// DeleteFinishedRequestsBefore 删除开始时间早于 cutoff 且不再运行的请求记录及其 Payload/SSE 子记录。
// 每次调用只在单个小事务内处理一个批次；调用方负责设置保留期限并决定下一批运行时间。
func (r *Repo) DeleteFinishedRequestsBefore(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("request log repository is not configured")
	}
	if batchSize < 1 || batchSize > maxRequestRetentionBatchSize {
		return 0, fmt.Errorf("retention batch size must be between 1 and %d", maxRequestRetentionBatchSize)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// 用 started_at 索引分页锁定已结束请求；不触碰长时间运行中的推理。
	rows, err := tx.QueryContext(ctx, `SELECT request_id
		FROM request_logs
		WHERE started_at < ? AND status <> 'running'
		ORDER BY started_at ASC, id ASC
		LIMIT ? FOR UPDATE`, cutoff, batchSize)
	if err != nil {
		return 0, err
	}
	requestIDs := make([]string, 0, batchSize)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		requestIDs = append(requestIDs, id)
	}
	rowErr := rows.Err()
	if err := rows.Close(); err != nil && rowErr == nil {
		rowErr = err
	}
	if rowErr != nil {
		return 0, rowErr
	}
	if len(requestIDs) == 0 {
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, nil
	}

	placeholders := strings.TrimRight(strings.Repeat("?,", len(requestIDs)), ",")
	args := make([]any, 0, len(requestIDs))
	for _, id := range requestIDs {
		args = append(args, id)
	}
	// 无外键级联，按子表到父表顺序显式删除，确保 Payload/SSE 不会残留。
	for _, table := range []string{"request_stream_events", "request_payloads"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE request_id IN ("+placeholders+")", args...); err != nil {
			return 0, err
		}
	}
	res, err := tx.ExecContext(ctx,
		"DELETE FROM request_logs WHERE request_id IN ("+placeholders+") AND started_at < ? AND status <> 'running'",
		append(args, cutoff)...)
	if err != nil {
		return 0, err
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}
