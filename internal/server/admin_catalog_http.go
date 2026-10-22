// server（admin_catalog_http）：模型发现搜索 API（HF / Ollama 库），与安装下载解耦。
package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/catalog"
	"github.com/whl/llm-gateway/internal/catalog/ollama"
)

func (s *Server) catalogService() *catalog.Service {
	return catalog.NewService(catalog.Config{
		HFBaseURL:        s.cfg.Download.HFBaseURL,
		HFUserAgent:      s.cfg.Download.UserAgent,
		OllamaLibraryURL: s.cfg.Download.OllamaLibraryURL,
		HTTPTimeout:      time.Duration(s.cfg.Download.CatalogTimeout) * time.Second,
	})
}

func (s *Server) catalogSearchAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method_not_allowed", "GET required")
		return
	}
	q := catalog.Query{
		Text:       r.URL.Query().Get("q"),
		Source:     r.URL.Query().Get("source"),
		Sort:       catalog.SortKey(strings.TrimSpace(r.URL.Query().Get("sort"))),
		Limit:      parseIntDefault(r.URL.Query().Get("limit"), 30),
		HFGGUFOnly: parseBoolDefault(r.URL.Query().Get("hf_gguf"), true),
	}
	if q.Sort != catalog.SortHot && q.Sort != catalog.SortNewest {
		q.Sort = catalog.SortHot
	}
	ctx := r.Context()
	items, err := s.catalogService().Search(ctx, q)
	if err != nil {
		jsonError(w, 502, "catalog_search_failed", err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"data": items})
}

func (s *Server) hfSearch(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method_not_allowed", "GET required")
		return
	}
	q := catalog.Query{
		Text:       r.URL.Query().Get("q"),
		Source:     "hf",
		Sort:       catalog.SortKey(strings.TrimSpace(r.URL.Query().Get("sort"))),
		Limit:      parseIntDefault(r.URL.Query().Get("limit"), 30),
		HFGGUFOnly: parseBoolDefault(r.URL.Query().Get("hf_gguf"), true),
	}
	if q.Text == "" {
		q.Text = r.URL.Query().Get("search")
	}
	if q.Sort != catalog.SortHot && q.Sort != catalog.SortNewest {
		q.Sort = catalog.SortHot
	}
	items, err := s.catalogService().Search(r.Context(), q)
	if err != nil {
		jsonError(w, 502, "huggingface_error", err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"data": items})
}

func parseBoolDefault(raw string, def bool) bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return def
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func (s *Server) ollamaCatalogTagsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		jsonError(w, 405, "method_not_allowed", "GET required")
		return
	}
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if model == "" {
		jsonError(w, 400, "model_required", "model query required")
		return
	}
	oc := &ollama.Client{
		BaseURL:   s.cfg.Download.OllamaLibraryURL,
		UserAgent: s.cfg.Download.UserAgent,
		HTTPClient: &http.Client{
			Timeout: time.Duration(s.cfg.Download.CatalogTimeout) * time.Second,
		},
	}
	tags, err := oc.ListTags(r.Context(), model)
	if err != nil {
		jsonError(w, 502, "ollama_tags_failed", err.Error())
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"model": model, "tags": tags})
}

func parseIntDefault(raw string, def int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
