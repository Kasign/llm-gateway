package core

import (
	"encoding/json"
	"strings"
	"time"
)

func parse(req Request) State {
	if req.Started.IsZero() {
		req.Started = time.Now()
	}
	body := append([]byte(nil), req.Body...)
	st := State{
		Request:      req,
		OriginalBody: body,
		UpstreamPath: req.Path,
		ClientModel:  parseModel(body),
		Stream:       parseStream(body) || wantsEventStream(req.HeaderGet("Accept")),
		ParsedAt:     time.Now(),
	}
	st.Body = body
	return st
}

func parseModel(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	return v.Model
}

func parseStream(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		return false
	}
	v, ok := obj["stream"]
	if !ok {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		return s == "true" || s == "1" || s == "yes"
	case float64:
		return t != 0
	default:
		return false
	}
}

func wantsEventStream(accept string) bool {
	return strings.Contains(strings.ToLower(accept), "text/event-stream")
}

// stampModel 保证发出去的 model 字段等于服务选定的上游 ID。
func stampModel(st State) State {
	id := strings.TrimSpace(st.Model.UpstreamID)
	if id == "" || len(st.Body) == 0 {
		return st
	}
	var obj map[string]any
	if json.Unmarshal(st.Body, &obj) != nil {
		return st
	}
	obj["model"] = id
	out, err := json.Marshal(obj)
	if err != nil {
		return st
	}
	st.Body = out
	return st
}
