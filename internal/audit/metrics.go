package audit

import "time"

// StatsSnapshot 是审计队列的只读聚合快照，不包含请求正文、凭据或原始数据库错误。
// 各计数器独立原子读取，快照是低成本近似值，不承诺跨字段事务一致性。
type StatsSnapshot struct {
	QueueDepth                 int     `json:"queue_depth"`
	QueueCapacity              int     `json:"queue_capacity"`
	NormalLimit                int     `json:"normal_limit"`
	Closed                     bool    `json:"closed"`
	EnqueuedTotal              uint64  `json:"enqueued_total"`
	AttemptedTotal             uint64  `json:"attempted_total"`
	DroppedTotal               uint64  `json:"dropped_total"`
	DroppedWriterClosedTotal   uint64  `json:"dropped_writer_closed_total"`
	DroppedNormalReservedTotal uint64  `json:"dropped_normal_capacity_reserved_total"`
	DroppedQueueFullTotal      uint64  `json:"dropped_queue_full_total"`
	DropRate                   float64 `json:"drop_rate"`
	WriteSucceededTotal        uint64  `json:"write_succeeded_total"`
	WriteFailedTotal           uint64  `json:"write_failed_total"`
	QueueWaitCount             uint64  `json:"queue_wait_count"`
	QueueWaitTotalMS           float64 `json:"queue_wait_total_ms"`
	QueueWaitMaxMS             float64 `json:"queue_wait_max_ms"`
}

// Stats 返回当前内存快照；不执行数据库查询，也不等待队列排空。
// drop_rate 是累计丢弃数 / (累计成功入队数 + 累计丢弃数)，范围为 0 到 1。
func (w *Writer) Stats() StatsSnapshot {
	if w == nil {
		return StatsSnapshot{}
	}
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()

	enqueued := w.enqueued.Load()
	dropped := w.dropped.Load()
	attempted := enqueued + dropped
	dropRate := float64(0)
	if attempted > 0 {
		dropRate = float64(dropped) / float64(attempted)
	}
	return StatsSnapshot{
		QueueDepth:                 len(w.ch),
		QueueCapacity:              cap(w.ch),
		NormalLimit:                w.normalLimit,
		Closed:                     closed,
		EnqueuedTotal:              enqueued,
		AttemptedTotal:             attempted,
		DroppedTotal:               dropped,
		DroppedWriterClosedTotal:   w.droppedWriterClosed.Load(),
		DroppedNormalReservedTotal: w.droppedNormalReserved.Load(),
		DroppedQueueFullTotal:      w.droppedQueueFull.Load(),
		DropRate:                   dropRate,
		WriteSucceededTotal:        w.writeSucceeded.Load(),
		WriteFailedTotal:           w.writeFailed.Load(),
		QueueWaitCount:             w.queueWaitCount.Load(),
		QueueWaitTotalMS:           float64(w.queueWaitTotalNS.Load()) / float64(time.Millisecond),
		QueueWaitMaxMS:             float64(w.queueWaitMaxNS.Load()) / float64(time.Millisecond),
	}
}
