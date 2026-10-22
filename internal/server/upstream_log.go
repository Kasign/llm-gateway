package server

import (
	"strings"

	"github.com/whl/llm-gateway/internal/applog"
)

const upstreamJSONLogMax = 4096

func formatUpstreamJSONBody(body []byte, err error) string {
	if err != nil {
		msg := strings.TrimSpace(err.Error())
		if len(body) > 0 {
			return msg + " body=" + truncateForLog(string(body), upstreamJSONLogMax)
		}
		return msg
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "(empty)"
	}
	return truncateForLog(s, upstreamJSONLogMax)
}

func truncateForLog(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func logUpstreamJSON(log InferenceOpLogger, action string, body []byte, err error) {
	line := formatUpstreamJSONBody(body, err)
	applog.Infof("%s upstream_json=%s", action, line)
	if log == nil {
		return
	}
	level := "info"
	if err != nil {
		level = "err"
	}
	log(level, action+" 上游响应 JSON: "+line)
}
