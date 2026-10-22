// llama 是网关与上游推理服务（llama.cpp 或 Ollama）的 HTTP 客户端：健康检查、加载/卸载模型、转发 OpenAI 兼容 API。
package llama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client 持有上游 base URL、后端类型与可选的上游 API Key（与网关发给用户的 Key 无关）。
type Client struct {
	base    string
	backend string
	apiKey  string
	http    *http.Client
}

// New 创建客户端；backend 为空时默认 llamacpp。
func New(base string, timeout time.Duration, backend, apiKey string) *Client {
	backend = strings.ToLower(strings.TrimSpace(backend))
	if backend == "" {
		backend = "llamacpp"
	}
	return &Client{
		base:    strings.TrimRight(base, "/"),
		backend: backend,
		apiKey:  strings.TrimSpace(apiKey),
		http:    &http.Client{Timeout: timeout},
	}
}

// setAuth adds Bearer token only when LLAMA_UPSTREAM_API_KEY is set (llama.cpp --api-key).
// Gateway admin / caller API keys are never sent here.
func (c *Client) setAuth(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

func (c *Client) IsOllama() bool { return c.backend == "ollama" }

// BaseURL returns the configured upstream root URL (for cache keys and logs).
func (c *Client) BaseURL() string { return c.base }

func (c *Client) Health(ctx context.Context) error {
	return c.Reachable(ctx)
}

func isUpstreamAuthError(status int, body []byte) bool {
	if status == 401 {
		return true
	}
	s := strings.ToLower(string(body))
	return strings.Contains(s, "invalid api key") || strings.Contains(s, "authentication_error")
}

func formatHTTPError(method, path string, status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 240 {
		msg = msg[:240] + "…"
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	base := fmt.Errorf("上游 %s %s 返回 HTTP %d：%s", method, path, status, msg)
	low := strings.ToLower(msg)
	if strings.Contains(low, "error code: 1003") || strings.Contains(low, "cloudflare") {
		return fmt.Errorf("%w（这是 Cloudflare/边缘代理返回的页面，不是 llama.cpp；请求多半没进容器。LLAMA_CPP_BASE_URL 请用 Docker 网内 HTTP，如 http://Llama-cpp:8080，勿用经 Cloudflare 的公网域名或裸 IP）", base)
	}
	if isUpstreamAuthError(status, body) {
		return fmt.Errorf("%w（与 Gateway 管理密钥、调用方 API Key 无关；仅当 llama.cpp 进程启用了 --api-key 时，才在 deploy/.env 配置 LLAMA_UPSTREAM_API_KEY）", base)
	}
	if status == 403 {
		return fmt.Errorf("%w（若 body 为纯文本而非 JSON，多为反代/WAF 拦截，不是「缺少接口」；请在 Gateway 容器内 curl 同一 URL 对比）", base)
	}
	return base
}

// Reachable checks that the configured upstream URL responds (health or model list).
func (c *Client) Reachable(ctx context.Context) error {
	if c.IsOllama() {
		return c.reachOllamaNative(ctx)
	}
	return c.reachByGET(ctx, []string{"/health", "/v1/health", "/models", "/v1/models"})
}

// ensureOllamaDaemon 校验本机 Ollama 上游（配置层 OLLAMA_BASE_URL）；与 download.ollama_library_url 无关。
func (c *Client) ensureOllamaDaemon(ctx context.Context) error {
	if !c.IsOllama() {
		return nil
	}
	return c.reachOllamaNative(ctx)
}

// reachOllamaNative 仅认 OLLAMA_BASE_URL 上的本机 Ollama daemon。
// 公网 ollama.com 对 /api/tags 也返回 200+models，但 /api/version 固定为 0.0.0（非真实 daemon）。
func (c *Client) reachOllamaNative(ctx context.Context) error {
	if err := errOllamaLibraryAsUpstream(c.base); err != nil {
		return err
	}
	stVer, bVer, errVer := c.rawGet(ctx, "/api/version")
	if errVer == nil && stVer < 300 {
		if ver, ok := parseOllamaVersion(bVer); ok {
			if ver == "0.0.0" {
				return fmt.Errorf(
					"上游 %s 的 /api/version 为 0.0.0，是公网模型库桩而非本机 Ollama；OLLAMA_BASE_URL 请指向本机实例（如 http://127.0.0.1:11434），浏览/下载用 download.ollama_library_url",
					c.base,
				)
			}
			if ver != "" {
				return c.reachOllamaTagsCheck(ctx)
			}
		}
	}
	return c.reachOllamaTagsAndPS(ctx)
}

func (c *Client) reachOllamaTagsCheck(ctx context.Context) error {
	status, b, err := c.rawGet(ctx, "/api/tags")
	if err != nil {
		return fmt.Errorf("连接 %s 失败：%w", c.base, err)
	}
	if status >= 300 {
		return formatHTTPError("GET", "/api/tags", status, b)
	}
	if !looksLikeOllamaTagsBody(b) {
		return fmt.Errorf("上游 %s 的 /api/tags 不是 Ollama 响应（若与 llama.cpp 共用地址，请单独配置 OLLAMA_BASE_URL）", c.base)
	}
	return nil
}

func (c *Client) reachOllamaTagsAndPS(ctx context.Context) error {
	if err := c.reachOllamaTagsCheck(ctx); err != nil {
		return err
	}
	status, b, err := c.rawGet(ctx, "/api/ps")
	if err != nil {
		return fmt.Errorf("连接 %s 失败：%w", c.base, err)
	}
	if status >= 300 {
		return formatHTTPError("GET", "/api/ps", status, b)
	}
	if !looksLikeOllamaPsBody(b) {
		return fmt.Errorf("上游 %s 的 /api/ps 不是 Ollama 响应（公网模型库代理或未启动的 Ollama）", c.base)
	}
	return nil
}

func (c *Client) reachByGET(ctx context.Context, paths []string) error {
	var last error
	for _, p := range paths {
		status, b, err := c.rawGet(ctx, p)
		if err != nil {
			last = fmt.Errorf("连接 %s 失败：%w", c.base, err)
			continue
		}
		if status < 300 {
			return nil
		}
		last = formatHTTPError("GET", p, status, b)
	}
	if last == nil {
		last = fmt.Errorf("无法访问上游 %s", c.base)
	}
	return last
}

// ServiceKind probes which service responds at base URL (may differ from configured backend).
func (c *Client) ServiceKind(ctx context.Context) string {
	if UpstreamURLIsOllamaLibrary(c.base) {
		return ""
	}
	if b, err := c.get(ctx, "/api/tags"); err == nil && looksLikeOllamaTagsBody(b) {
		if c.ollamaVersionLooksLikePublicLibrary(ctx) {
			return ""
		}
		return "ollama"
	}
	if _, err := c.get(ctx, "/models"); err == nil {
		return "llamacpp"
	}
	if _, err := c.get(ctx, "/v1/models"); err == nil {
		return "llamacpp"
	}
	if _, err := c.get(ctx, "/health"); err == nil {
		return "llamacpp"
	}
	return ""
}

func (c *Client) List(ctx context.Context) (json.RawMessage, error) {
	if c.IsOllama() {
		if err := c.ensureOllamaDaemon(ctx); err != nil {
			return nil, err
		}
		return c.get(ctx, "/api/tags")
	}
	b, err := c.get(ctx, "/models")
	if err == nil {
		return b, nil
	}
	return c.get(ctx, "/v1/models")
}

func (c *Client) ListModelIDs(ctx context.Context) ([]string, error) {
	b, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	ids, _ := parseModelIDs(b)
	if len(ids) == 0 {
		ids = parseOllamaTagNames(b)
	}
	return ids, nil
}

// RouterModelEntry is one model row from llama.cpp GET /models (router mode).
type RouterModelEntry struct {
	ID       string
	Aliases  []string
	Status   string
	Running  bool
	InMemory bool
}

func (c *Client) LlamaRouterModels(ctx context.Context) ([]RouterModelEntry, error) {
	if c.IsOllama() {
		return nil, nil
	}
	b, err := c.get(ctx, "/models")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []struct {
			ID      string          `json:"id"`
			Aliases []string        `json:"aliases"`
			Status  json.RawMessage `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, err
	}
	out := make([]RouterModelEntry, 0, len(resp.Data))
	for _, item := range resp.Data {
		if item.ID == "" {
			continue
		}
		st := parseLlamaStatus(item.Status)
		out = append(out, RouterModelEntry{
			ID:       item.ID,
			Aliases:  item.Aliases,
			Status:   st.display,
			Running:  st.running,
			InMemory: st.inMemory,
		})
	}
	return out, nil
}

func (c *Client) HasRouter(ctx context.Context) (bool, error) {
	entries, err := c.LlamaRouterModels(ctx)
	if err != nil || len(entries) == 0 {
		return false, nil
	}
	for _, e := range entries {
		if e.Status != "" && strings.ToLower(e.Status) != "unknown" {
			return true, nil
		}
	}
	return len(entries) > 0, nil
}

func (c *Client) LlamaUnloadCandidates(modelID string, entries []RouterModelEntry) []string {
	modelID = strings.TrimSpace(modelID)
	seen := map[string]struct{}{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, e := range entries {
		if ModelIDMatch(e.ID, modelID) {
			add(e.ID)
			for _, a := range e.Aliases {
				add(a)
			}
		}
	}
	add(modelID)
	return out
}

func (c *Client) LlamaLoadCandidatesForEntry(modelID string, entries []RouterModelEntry) []string {
	return c.LlamaUnloadCandidates(modelID, entries)
}

func (c *Client) UnloadLlamaRouter(ctx context.Context, modelID string) error {
	_, err := c.UnloadFromMemory(ctx, modelID)
	return err
}

func modelInRouterMemory(modelID string, entries []RouterModelEntry) bool {
	for _, e := range entries {
		if ModelIDMatch(e.ID, modelID) && e.InMemory {
			return true
		}
	}
	return false
}

// ModelLoadedInMemory reports whether modelID is already in upstream memory, using the same
// sources as RuntimeSnapshot (GET /models status, Ollama /api/ps) plus router InMemory flags.
// WaitUntilModelReady blocks until modelID is usable for inference (loaded in upstream memory).
// On non-router llama.cpp it returns immediately; Ollama waits until /api/ps lists the model.
// modelReadyPollMax 轮询上游「模型进内存」的上限；超时后由调用方决定是否仍发起 chat。
const modelReadyPollMax = 5 * time.Minute

func (c *Client) WaitUntilModelReady(ctx context.Context, modelID string) error {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	if ok, _ := c.ModelLoadedInMemoryRefresh(ctx, modelID); ok {
		return nil
	}
	if !c.IsOllama() {
		hasRouter, err := c.HasRouter(ctx)
		if err != nil || !hasRouter {
			return nil
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, modelReadyPollMax)
	defer cancel()
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	poll := 0
	for {
		reload := poll == 0 || poll%5 == 0
		poll++
		if ok, _ := c.modelLoadedInMemory(waitCtx, modelID, reload); ok {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("model %q still loading after %s: %w", modelID, modelReadyPollMax, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// ModelLoadedInMemory 查询上游快照；reload=false 时不带 ?reload=1，避免每次 API 请求都扫盘。
func (c *Client) ModelLoadedInMemory(ctx context.Context, modelID string) (bool, string) {
	return c.modelLoadedInMemory(ctx, modelID, false)
}

// ModelLoadedInMemoryRefresh 在 load/轮询等待前用，会 GET /models?reload=1（较慢，更准）。
func (c *Client) ModelLoadedInMemoryRefresh(ctx context.Context, modelID string) (bool, string) {
	return c.modelLoadedInMemory(ctx, modelID, true)
}

func (c *Client) modelLoadedInMemory(ctx context.Context, modelID string, reload bool) (bool, string) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return false, ""
	}
	if reload {
		_, _ = c.Refresh(ctx)
	}
	snap, err := c.RuntimeSnapshot(ctx, modelID)
	if err == nil && snap != nil {
		for _, item := range snap.Loaded {
			if ModelIDMatch(item.ID, modelID) {
				return true, item.ID
			}
		}
		for _, item := range snap.Catalog {
			if item.Loaded && ModelIDMatch(item.ID, modelID) {
				return true, item.ID
			}
		}
	}
	if !c.IsOllama() {
		entries, err := c.LlamaRouterModels(ctx)
		if err == nil {
			for _, e := range entries {
				if ModelIDMatch(e.ID, modelID) && e.InMemory {
					return true, e.ID
				}
			}
		}
	}
	return false, ""
}

// UnloadFromMemory stops using a model in the inference service without deleting files on disk.
// llama.cpp: POST /models/unload (router). Ollama: keep_alive=0.
func (c *Client) UnloadFromMemory(ctx context.Context, modelID string) ([]byte, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil, fmt.Errorf("model id required")
	}
	if c.IsOllama() {
		body, err := c.Unload(ctx, modelID)
		if err != nil && !UpstreamModelAbsent(err) {
			return body, err
		}
		return body, nil
	}
	entries, err := c.LlamaRouterModels(ctx)
	if err != nil {
		return nil, fmt.Errorf(
			"llama.cpp 未暴露 GET /models（非 router 或版本过旧）：无法在不停进程的情况下释放模型内存；GGUF 仍在磁盘，需重启推理服务或改用带 model router 的 llama-server",
		)
	}
	if !modelInRouterMemory(modelID, entries) {
		return nil, nil
	}

	const rounds = 4
	var lastHard error
	var lastBody []byte
	for r := 0; r < rounds; r++ {
		entries, err = c.LlamaRouterModels(ctx)
		if err != nil {
			return lastBody, err
		}
		if !modelInRouterMemory(modelID, entries) {
			return lastBody, nil
		}
		candidates := c.LlamaUnloadCandidates(modelID, entries)
		if len(candidates) == 0 {
			candidates = []string{modelID}
		}
		for _, name := range candidates {
			if body, err := c.Unload(ctx, name); err != nil {
				if UpstreamModelAbsent(err) || isLlamaUnloadRetryable(err) {
					continue
				}
				lastHard = err
			} else if len(body) > 0 {
				lastBody = body
			}
		}
		_, _ = c.Refresh(ctx)
	}
	entries, _ = c.LlamaRouterModels(ctx)
	if modelInRouterMemory(modelID, entries) {
		if lastHard != nil {
			return lastBody, fmt.Errorf("卸载后仍在内存（loaded/sleeping）: %w", lastHard)
		}
		return lastBody, fmt.Errorf("卸载后仍在内存（loaded/sleeping）；可试「卸载全部」或检查 llama.cpp router 版本")
	}
	return lastBody, nil
}

// UpstreamModelAbsent reports whether the upstream has no such model loaded (unload is a no-op).
func UpstreamModelAbsent(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "not running") ||
		strings.Contains(msg, "model is not found") ||
		strings.Contains(msg, "model is not running")
}

func isLlamaUnloadRetryable(err error) bool {
	if UpstreamModelAbsent(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid_request")
}

func parseModelIDs(b []byte) ([]string, error) {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("parse llama models list: %w", err)
	}
	ids := make([]string, 0, len(resp.Data))
	for _, item := range resp.Data {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	return ids, nil
}
func looksLikeOllamaTagsBody(b []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	_, ok := m["models"]
	return ok
}

func looksLikeOllamaPsBody(b []byte) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	_, ok := m["models"]
	return ok
}

func parseOllamaVersion(b []byte) (string, bool) {
	var v struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &v) != nil {
		return "", false
	}
	return strings.TrimSpace(v.Version), true
}

func (c *Client) ollamaVersionLooksLikePublicLibrary(ctx context.Context) bool {
	st, b, err := c.rawGet(ctx, "/api/version")
	if err != nil || st >= 300 {
		return false
	}
	ver, ok := parseOllamaVersion(b)
	return ok && ver == "0.0.0"
}

// UpstreamURLIsOllamaLibrary 为公网模型库域名，只应出现在 download.ollama_library_url，不能作 OLLAMA_BASE_URL。
func UpstreamURLIsOllamaLibrary(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "ollama.com", "www.ollama.com":
		return true
	default:
		return false
	}
}

func errOllamaLibraryAsUpstream(base string) error {
	if !UpstreamURLIsOllamaLibrary(base) {
		return nil
	}
	return fmt.Errorf(
		"OLLAMA_BASE_URL 不能指向公网模型库 (%s)；本机 Ollama 请填如 http://127.0.0.1:11434，浏览/下载模型用 download.ollama_library_url",
		base,
	)
}

func parseOllamaTagNames(b []byte) []string {
	var resp struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &resp) != nil {
		return nil
	}
	ids := make([]string, 0, len(resp.Models))
	for _, item := range resp.Models {
		name := item.Name
		if name == "" {
			name = item.Model
		}
		if name != "" {
			ids = append(ids, name)
		}
	}
	return ids
}

func (c *Client) Refresh(ctx context.Context) (json.RawMessage, error) {
	if c.IsOllama() {
		return c.List(ctx)
	}
	return c.get(ctx, "/models?reload=1")
}
func (c *Client) Load(ctx context.Context, name, keepAlive string) ([]byte, error) {
	if c.IsOllama() {
		if err := c.ensureOllamaDaemon(ctx); err != nil {
			return nil, err
		}
		if strings.TrimSpace(keepAlive) == "" {
			keepAlive = "24h"
		}
		return c.postJSON(ctx, "/api/generate", map[string]interface{}{
			"model":      name,
			"prompt":     "",
			"keep_alive": keepAlive,
		})
	}
	return c.postJSON(ctx, "/models/load", map[string]string{"model": name})
}
func (c *Client) Unload(ctx context.Context, name string) ([]byte, error) {
	if c.IsOllama() {
		if err := c.ensureOllamaDaemon(ctx); err != nil {
			return nil, err
		}
		return c.postJSON(ctx, "/api/generate", map[string]interface{}{
			"model":      name,
			"prompt":     "",
			"keep_alive": 0,
		})
	}
	return c.postJSON(ctx, "/models/unload", map[string]string{"model": name})
}

// OllamaPull streams POST /api/pull and reports layer progress via onProgress (done/total bytes).
func (c *Client) OllamaPull(ctx context.Context, name string, onProgress func(done, total int64, status string)) error {
	if err := c.ensureOllamaDaemon(ctx); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name required")
	}
	payload, _ := json.Marshal(map[string]interface{}{"name": name, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/pull", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	// Pull can run long; rely on ctx cancel, not Client.Timeout.
	pullHTTP := &http.Client{Transport: c.http.Transport}
	resp, err := pullHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		bb, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return formatHTTPError("POST", "/api/pull", resp.StatusCode, bb)
	}
	dec := json.NewDecoder(resp.Body)
	var lastErr string
	for dec.More() {
		var ev struct {
			Status    string `json:"status"`
			Error     string `json:"error"`
			Completed int64  `json:"completed"`
			Total     int64  `json:"total"`
		}
		if err := dec.Decode(&ev); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if ev.Error != "" {
			lastErr = ev.Error
		}
		if onProgress != nil && ev.Total > 0 {
			onProgress(ev.Completed, ev.Total, ev.Status)
		} else if onProgress != nil && ev.Status != "" {
			onProgress(0, 0, ev.Status)
		}
		switch ev.Status {
		case "success":
			return nil
		case "error":
			if lastErr != "" {
				return fmt.Errorf("%s", lastErr)
			}
			return fmt.Errorf("ollama pull failed")
		}
	}
	if lastErr != "" {
		return fmt.Errorf("%s", lastErr)
	}
	return nil
}

func (c *Client) OllamaDeleteModel(ctx context.Context, name string) ([]byte, error) {
	if err := c.ensureOllamaDaemon(ctx); err != nil {
		return nil, err
	}
	// Ollama 要求 DELETE（POST 会 405）；新旧版本字段分别为 model / name，一并发送。
	return c.deleteJSON(ctx, "/api/delete", map[string]string{"model": name, "name": name})
}

// UnloadAll releases upstream GPU/RAM for all loaded models (best effort).
func (c *Client) UnloadAll(ctx context.Context) error {
	if c.IsOllama() {
		if err := c.ensureOllamaDaemon(ctx); err != nil {
			return err
		}
		b, err := c.get(ctx, "/api/ps")
		if err != nil {
			return err
		}
		var ps struct {
			Models []struct {
				Name  string `json:"name"`
				Model string `json:"model"`
			} `json:"models"`
		}
		if json.Unmarshal(b, &ps) != nil || len(ps.Models) == 0 {
			return nil
		}
		var last error
		for _, item := range ps.Models {
			name := item.Name
			if name == "" {
				name = item.Model
			}
			if name == "" {
				continue
			}
			if _, err := c.Unload(ctx, name); err != nil {
				last = err
			}
		}
		return last
	}
	entries, err := c.LlamaRouterModels(ctx)
	if err != nil {
		ids, listErr := c.ListModelIDs(ctx)
		if listErr != nil {
			return listErr
		}
		var last error
		for _, id := range ids {
			if _, err := c.Unload(ctx, id); err != nil && !UpstreamModelAbsent(err) {
				last = err
			}
		}
		return last
	}
	var last error
	for _, e := range entries {
		if !e.InMemory {
			continue
		}
		names := append([]string{e.ID}, e.Aliases...)
		for _, name := range names {
			if _, err := c.Unload(ctx, name); err != nil && !UpstreamModelAbsent(err) {
				last = err
			}
		}
	}
	_, _ = c.Refresh(ctx)
	return last
}
func (c *Client) Delete(ctx context.Context, name string) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/models?model="+url.QueryEscape(name), nil)
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("llama delete: %s", strings.TrimSpace(string(b)))
	}
	return nil
}

// Proxy 非流式 chat：向 c.base+path（如 /v1/chat/completions）发 POST，读完整响应后写回客户端（出网点）。
func (c *Client) Proxy(ctx context.Context, w http.ResponseWriter, r *http.Request, path string, body io.Reader) (int64, []byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, r.Method, c.base+path, body)
	if err != nil {
		return 0, nil, 0, err
	}
	copyRequestHeaders(req.Header, r.Header)
	c.setUpstreamAuthIfConfigured(req)
	emitOutboundHeaders(ctx, req)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, 0, err
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return int64(len(b)), b, resp.StatusCode, err
	}
	w.WriteHeader(resp.StatusCode)
	_, err = w.Write(b)
	return int64(len(b)), b, resp.StatusCode, err
}

// StreamProxy 流式 chat：同样 POST 上游，32KB 一块读 upstream body，立刻 Flush 给客户端（SSE 透传）。
// clientWriter 非 nil 时替代默认 w.Write。
func (c *Client) StreamProxy(ctx context.Context, w http.ResponseWriter, r *http.Request, path string, body io.Reader, onChunk func([]byte), onHeader func(http.Header, int), clientWriter func([]byte) error) error {
	req, err := http.NewRequestWithContext(ctx, r.Method, c.base+path, body)
	if err != nil {
		return err
	}
	copyRequestHeaders(req.Header, r.Header)
	c.setUpstreamAuthIfConfigured(req)
	emitOutboundHeaders(ctx, req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	if onHeader != nil {
		onHeader(resp.Header, resp.StatusCode)
	}
	w.WriteHeader(resp.StatusCode)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	writeOut := clientWriter
	if writeOut == nil {
		writeOut = func(b []byte) error {
			_, err := w.Write(b)
			return err
		}
	}
	buf := make([]byte, 32*1024)
	for {
		n, e := resp.Body.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			if err := writeOut(chunk); err != nil {
				return err
			}
			// 先把当前数据块 Flush 给调用方，再运行同步观察器，避免日志解析延迟本块交付。
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if onChunk != nil {
				onChunk(chunk)
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	return nil
}

// copyRequestHeaders 转发客户端 Header 到上游，但故意不带 Authorization/X-API-Key：
// 网关发给用户的 Key 绝不能原样打到 llama.cpp；上游密钥由 setUpstreamAuthIfConfigured 单独设置。
func copyRequestHeaders(dst, src http.Header) {
	for k, v := range src {
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "X-API-Key") || strings.EqualFold(k, "X-Admin-Key") || strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Transfer-Encoding") || strings.EqualFold(k, "Connection") || strings.EqualFold(k, "Host") {
			continue
		}
		for _, x := range v {
			dst.Add(k, x)
		}
	}
}

func (c *Client) setUpstreamAuthIfConfigured(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}

// Probe performs GET path and returns status plus a short body sample (for admin diagnostics).
func (c *Client) Probe(ctx context.Context, path string) (status int, bodySample string, netErr error) {
	status, b, netErr := c.rawGet(ctx, path)
	if netErr != nil {
		return 0, "", netErr
	}
	sample := strings.TrimSpace(string(b))
	if len(sample) > 160 {
		sample = sample[:160] + "…"
	}
	return status, sample, nil
}

func (c *Client) rawGet(ctx context.Context, p string) (int, []byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+p, nil)
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

func (c *Client) get(ctx context.Context, p string) (json.RawMessage, error) {
	status, b, err := c.rawGet(ctx, p)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, formatHTTPError("GET", p, status, b)
	}
	return b, nil
}
func (c *Client) postJSON(ctx context.Context, p string, v interface{}) ([]byte, error) {
	b, _ := json.Marshal(v)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+p, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	bb, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if resp.StatusCode >= 300 {
		return bb, formatHTTPError("POST", p, resp.StatusCode, bb)
	}
	return bb, nil
}

func (c *Client) deleteJSON(ctx context.Context, p string, v interface{}) ([]byte, error) {
	b, _ := json.Marshal(v)
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+p, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	bb, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if resp.StatusCode >= 300 {
		return bb, formatHTTPError("DELETE", p, resp.StatusCode, bb)
	}
	return bb, nil
}
func (c *Client) RawJSON(ctx context.Context, path string, body []byte) ([]byte, int, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, resp.StatusCode, err
}

// RawStream POSTs JSON and reads the upstream response body incrementally (SSE/chunked).
func (c *Client) RawStream(ctx context.Context, path string, body []byte, onChunk func([]byte)) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	var full []byte
	buf := make([]byte, 32*1024)
	for {
		n, e := resp.Body.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			full = append(full, chunk...)
			if onChunk != nil {
				onChunk(chunk)
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return full, resp.StatusCode, e
		}
	}
	return full, resp.StatusCode, nil
}
func copyHeaders(dst, src http.Header) {
	for k, v := range src {
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Transfer-Encoding") || strings.EqualFold(k, "Connection") {
			continue
		}
		for _, x := range v {
			dst.Add(k, x)
		}
	}
}
