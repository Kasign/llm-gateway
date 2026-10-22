// server（model_ops）：统一的 POST /api/admin/model-ops 入口，带步骤日志返回给前端。
package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/applog"
)

type opLogLine struct {
	At      string `json:"at"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type opLogger struct {
	lines []opLogLine
}

func (l *opLogger) add(level, msg string) {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		level = "info"
	}
	l.lines = append(l.lines, opLogLine{
		At:      time.Now().UTC().Format(time.RFC3339Nano),
		Level:   level,
		Message: msg,
	})
}

func (s *Server) modelOpsTestAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonError(w, 405, "method_not_allowed", "POST required")
		return
	}
	var in struct {
		Backend  string `json:"backend"`
		Action   string `json:"action"`
		ModelID  string `json:"model_id"`
		ModelKey string `json:"model_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonError(w, 400, "invalid_body", err.Error())
		return
	}
	backend := strings.ToLower(strings.TrimSpace(in.Backend))
	action := strings.ToLower(strings.TrimSpace(in.Action))
	if backend != "llamacpp" && backend != "ollama" {
		jsonError(w, 400, "invalid_backend", "backend must be llamacpp or ollama")
		return
	}
	if action != "list" && action != "load" && action != "unload" {
		jsonError(w, 400, "invalid_action", "action must be list, load, or unload")
		return
	}
	applog.Infof("model-ops: backend=%s action=%s model_id=%q model_key=%q", backend, action, strings.TrimSpace(in.ModelID), strings.TrimSpace(in.ModelKey))

	log := &opLogger{}
	ctx := r.Context()
	log.add("info", "开始 action="+action+" backend="+backend)
	log.add("info", "入口 unified model-ops（与各 Tab 加载/卸载相同）")

	client, baseURL, err := s.adminClientForBackend(backend)
	if err != nil {
		log.add("err", err.Error())
		writeModelOpsResult(w, false, log, err.Error())
		return
	}
	log.add("info", "上游地址 "+baseURL)

	modelID := strings.TrimSpace(in.ModelID)
	modelKey := strings.TrimSpace(in.ModelKey)
	if modelKey != "" {
		m, err := s.models.GetByKey(ctx, modelKey)
		if err != nil {
			log.add("err", "Gateway 模型 "+modelKey+" 不存在")
			writeModelOpsResult(w, false, log, "model_key not found")
			return
		}
		log.add("info", "Gateway 记录 model_key="+m.ModelKey+" file="+m.Filename)
		if modelID == "" {
			if name, err := s.resolveUpstreamModelNameForBackend(ctx, client, backend, s.activeModelHintForBackend(backend), m); err == nil {
				modelID = name
				log.add("info", "解析上游名为 "+modelID)
			} else {
				log.add("warn", "解析上游名失败: "+err.Error())
				modelID = strings.TrimSuffix(m.LlamaModelName, ".gguf")
				if modelID == "" {
					modelID = m.Filename
				}
				log.add("info", "回退使用 "+modelID)
			}
		}
	}
	opLog := func(level, msg string) { log.add(level, msg) }

	ids, listErr := client.ListModelIDs(ctx)
	if listErr != nil {
		log.add("warn", "拉取模型列表失败: "+listErr.Error())
	} else {
		log.add("info", "上游 registry 共 "+strconv.Itoa(len(ids))+" 个: "+strings.Join(ids, ", "))
	}

	switch action {
	case "list":
		list, listErr := s.InferenceList(ctx, backend, false)
		if listErr != nil {
			log.add("err", "InferenceList: "+listErr.Error())
			writeModelOpsResult(w, false, log, listErr.Error())
			return
		}
		if list.Snapshot == nil {
			writeModelOpsResult(w, false, log, "empty upstream snapshot")
			return
		}
		for _, item := range list.Snapshot.Catalog {
			log.add("info", "· id="+item.ID+" status="+item.Status+" loaded="+boolStr(item.Loaded))
		}
		writeModelOpsResult(w, true, log, "")
		return
	}

	if modelID == "" && modelKey == "" {
		log.add("err", "需要 model_id 或 model_key")
		writeModelOpsResult(w, false, log, "model_id required")
		return
	}

	switch action {
	case "load":
		if _, err := s.InferenceLoad(ctx, backend, modelID, modelKey, opLog); err != nil {
			writeModelOpsResult(w, false, log, err.Error())
			return
		}
	case "unload":
		if _, err := s.InferenceUnload(ctx, backend, modelID, modelKey, opLog); err != nil {
			writeModelOpsResult(w, false, log, err.Error())
			return
		}
	}

	if ids2, err := client.ListModelIDs(ctx); err == nil {
		log.add("info", "操作后 registry: "+strings.Join(ids2, ", "))
	}
	if list, err := s.InferenceList(ctx, backend, false); err == nil && list.Snapshot != nil {
		var loaded []string
		for _, item := range list.Snapshot.Loaded {
			loaded = append(loaded, item.ID)
		}
		log.add("info", "操作后在内存: "+strings.Join(loaded, ", "))
	}

	writeModelOpsResult(w, true, log, "")
}

func writeModelOpsResult(w http.ResponseWriter, ok bool, log *opLogger, errMsg string) {
	w.Header().Set("Content-Type", "application/json")
	out := map[string]interface{}{
		"success": ok,
		"logs":    log.lines,
	}
	if errMsg != "" && !ok {
		out["error"] = errMsg
	}
	json.NewEncoder(w).Encode(out)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func boolStr(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
