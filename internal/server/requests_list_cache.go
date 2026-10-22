// server（requests_list_cache）：调用记录列表短期内存缓存，减轻管理台轮询对 MariaDB 的压力。
package server

import (
	"context"
	"fmt"
	"time"

	"github.com/whl/llm-gateway/internal/store"
)

const requestsListCacheTTL = 3 * time.Second

type requestsListCacheEntry struct {
	at   time.Time
	logs []store.RequestLog
}

func (s *Server) requestsListCacheKey(limit int, requestID, modelName, status, keyName string) string {
	return fmt.Sprintf("%d|%s|%s|%s|%s", limit, requestID, modelName, status, keyName)
}

func (s *Server) queryRequestLogsCached(ctx context.Context, limit int, requestID, modelName, status, keyName string) ([]store.RequestLog, error) {
	// 单条 id 查询不缓存（详情/精确查）。
	if requestID != "" {
		return s.logs.Query(ctx, limit, requestID, modelName, status, keyName)
	}
	key := s.requestsListCacheKey(limit, requestID, modelName, status, keyName)
	s.reqListCacheMu.Lock()
	if s.reqListCache != nil {
		if ent, ok := s.reqListCache[key]; ok && time.Since(ent.at) < requestsListCacheTTL {
			out := append([]store.RequestLog(nil), ent.logs...)
			s.reqListCacheMu.Unlock()
			return out, nil
		}
	}
	s.reqListCacheMu.Unlock()

	logs, err := s.logs.Query(ctx, limit, requestID, modelName, status, keyName)
	if err != nil {
		return nil, err
	}
	s.reqListCacheMu.Lock()
	if s.reqListCache == nil {
		s.reqListCache = make(map[string]requestsListCacheEntry)
	}
	s.reqListCache[key] = requestsListCacheEntry{at: time.Now(), logs: append([]store.RequestLog(nil), logs...)}
	s.reqListCacheMu.Unlock()
	return logs, nil
}

func (s *Server) invalidateRequestListCache() {
	s.reqListCacheMu.Lock()
	s.reqListCache = nil
	s.reqListCacheMu.Unlock()
}
