// llama（runtime）：汇总上游「磁盘上有哪些模型、内存里加载了哪些、占多少显存/内存」供管理台展示。
package llama

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// CatalogItem describes one upstream model (installed or loaded).
type CatalogItem struct {
	ID         string `json:"id"`
	Loaded     bool   `json:"loaded"`
	Running    bool   `json:"running,omitempty"`
	Active     bool   `json:"active"`
	Status     string `json:"status,omitempty"`
	DiskBytes     int64  `json:"disk_bytes,omitempty"`
	VRAMBytes     int64  `json:"vram_bytes,omitempty"`
	RAMBytes      int64  `json:"ram_bytes,omitempty"`
	MemoryNote    string `json:"memory_note,omitempty"`
	Quantization  string `json:"quantization,omitempty"`
	ParameterSize string `json:"parameter_size,omitempty"`
}

// RuntimeSnapshot 是当前上游的一次快照：目录列表 + 已加载列表 + 合计显存/内存。
// RuntimeSnapshot summarizes upstream model catalog and memory usage.
type RuntimeSnapshot struct {
	Backend      string        `json:"backend"`
	ActiveModel  string        `json:"active_model"`
	Loaded       []CatalogItem `json:"loaded"`
	Catalog      []CatalogItem `json:"catalog"`
	LoadedVRAM   int64         `json:"loaded_vram_bytes,omitempty"`
	LoadedRAM    int64         `json:"loaded_ram_bytes,omitempty"`
}

// RuntimeSnapshot 按后端调用不同 API（Ollama tags/ps 或 llama.cpp GET /models，不扫盘）。
func (c *Client) RuntimeSnapshot(ctx context.Context, activeModel string) (*RuntimeSnapshot, error) {
	return c.runtimeSnapshot(ctx, activeModel, false)
}

// RuntimeSnapshotInMemoryOnly 仅查询内存中的模型（Ollama 只 GET /api/ps，不打 /api/tags）。
func (c *Client) RuntimeSnapshotInMemoryOnly(ctx context.Context, activeModel string) (*RuntimeSnapshot, error) {
	activeModel = strings.TrimSpace(activeModel)
	if c.IsOllama() {
		return c.ollamaRuntimeInMemory(ctx, activeModel)
	}
	return c.runtimeSnapshot(ctx, activeModel, false)
}

// RuntimeSnapshotRescan 在需要刷新 llama 磁盘注册表时调用（GET /models?reload=1 后再拉列表）。
func (c *Client) RuntimeSnapshotRescan(ctx context.Context, activeModel string) (*RuntimeSnapshot, error) {
	return c.runtimeSnapshot(ctx, activeModel, true)
}

func (c *Client) runtimeSnapshot(ctx context.Context, activeModel string, rescanDisk bool) (*RuntimeSnapshot, error) {
	activeModel = strings.TrimSpace(activeModel)
	if c.IsOllama() {
		return c.ollamaRuntime(ctx, activeModel)
	}
	return c.llamacppRuntime(ctx, activeModel, rescanDisk)
}

func (c *Client) ollamaRuntime(ctx context.Context, activeModel string) (*RuntimeSnapshot, error) {
	out := &RuntimeSnapshot{Backend: "ollama", ActiveModel: activeModel}
	// 不在快照热路径做 ensureOllamaDaemon（/v1 代理 auto_load、轮询等）；写操作与 Reachable 仍校验。
	tagBytes, err := c.get(ctx, "/api/tags")
	if err != nil {
		return nil, err
	}
	diskByName := map[string]int64{}
	var tags struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
			Size  int64  `json:"size"`
			Details struct {
				ParameterSize     string `json:"parameter_size"`
				QuantizationLevel string `json:"quantization_level"`
			} `json:"details"`
		} `json:"models"`
	}
	_ = json.Unmarshal(tagBytes, &tags)
	for _, m := range tags.Models {
		name := m.Name
		if name == "" {
			name = m.Model
		}
		if name != "" {
			diskByName[name] = m.Size
			quant, param := ollamaCatalogMeta(m.Details.QuantizationLevel, m.Details.ParameterSize, name)
			out.Catalog = append(out.Catalog, CatalogItem{
				ID:            name,
				DiskBytes:     m.Size,
				Quantization:  quant,
				ParameterSize: param,
				Active:        activeModel != "" && (name == activeModel || strings.HasPrefix(name, activeModel+":")),
			})
		}
	}

	_ = c.mergeOllamaPsIntoSnapshot(ctx, out, diskByName, activeModel, true, false)
	ollamaFinishActiveModel(out, activeModel)
	return out, nil
}

func (c *Client) ollamaRuntimeInMemory(ctx context.Context, activeModel string) (*RuntimeSnapshot, error) {
	out := &RuntimeSnapshot{Backend: "ollama", ActiveModel: activeModel}
	if err := c.mergeOllamaPsIntoSnapshot(ctx, out, nil, activeModel, false, true); err != nil {
		return nil, err
	}
	ollamaFinishActiveModel(out, activeModel)
	return out, nil
}

func ollamaFinishActiveModel(out *RuntimeSnapshot, activeModel string) {
	if out == nil {
		return
	}
	if activeModel == "" && len(out.Loaded) == 1 {
		out.ActiveModel = out.Loaded[0].ID
	}
	if activeModel == "" && len(out.Loaded) > 0 {
		out.ActiveModel = out.Loaded[0].ID
	}
}

// mergeOllamaPsIntoSnapshot fills Loaded (+ optional Catalog loaded flags). trackBytes 为 false 时不累计 RAM/VRAM。
func (c *Client) mergeOllamaPsIntoSnapshot(ctx context.Context, out *RuntimeSnapshot, diskByName map[string]int64, activeModel string, trackBytes, psRequired bool) error {
	psBytes, err := c.get(ctx, "/api/ps")
	if err != nil {
		if psRequired {
			return err
		}
		return nil
	}
	var ps struct {
		Models []struct {
			Name     string `json:"name"`
			Model    string `json:"model"`
			Size     int64  `json:"size"`
			SizeVRAM int64  `json:"size_vram"`
		} `json:"models"`
	}
	if json.Unmarshal(psBytes, &ps) != nil {
		return nil
	}
	for _, m := range ps.Models {
		name := m.Name
		if name == "" {
			name = m.Model
		}
		if name == "" {
			continue
		}
		ram := m.Size - m.SizeVRAM
		if ram < 0 {
			ram = 0
		}
		item := CatalogItem{
			ID:     name,
			Loaded: true,
			Status: "loaded",
			Active: activeModel != "" && (name == activeModel || strings.HasPrefix(name, activeModel+":")),
		}
		if trackBytes {
			item.VRAMBytes = m.SizeVRAM
			item.RAMBytes = ram
			item.MemoryNote = "Ollama 运行中：VRAM + 主机 RAM 估算"
			if diskByName != nil {
				item.DiskBytes = diskByName[name]
			}
			if item.DiskBytes == 0 {
				item.DiskBytes = m.Size
			}
			out.LoadedVRAM += m.SizeVRAM
			out.LoadedRAM += ram
		}
		out.Loaded = append(out.Loaded, item)
		if diskByName == nil {
			continue
		}
		for i := range out.Catalog {
			if ollamaSameModel(out.Catalog[i].ID, name) {
				out.Catalog[i].Loaded = true
				out.Catalog[i].Status = "loaded"
				out.Catalog[i].Active = item.Active
				if trackBytes {
					out.Catalog[i].VRAMBytes = m.SizeVRAM
					out.Catalog[i].RAMBytes = ram
					out.Catalog[i].MemoryNote = item.MemoryNote
					if out.Catalog[i].DiskBytes == 0 {
						out.Catalog[i].DiskBytes = item.DiskBytes
					}
				}
			}
		}
	}
	return nil
}

var (
	ollamaQuantFromNameRe = regexp.MustCompile(`(?i)(?:^|[-_:])(q[0-9][a-z0-9_\-]*|f16|f32|bf16)(?:$|[-_:])`)
	ollamaQuantLooseRe    = regexp.MustCompile(`(?i)(Q[0-9][A-Z0-9_\-]*)`)
)

// ollamaCatalogMeta normalizes Ollama /api/tags details; falls back to parsing the model tag name.
func ollamaCatalogMeta(quantLevel, paramSize, modelName string) (quantization, parameterSize string) {
	quantization = strings.TrimSpace(quantLevel)
	parameterSize = strings.TrimSpace(paramSize)
	if quantization == "" {
		quantization = ollamaQuantFromName(modelName)
	}
	return quantization, parameterSize
}

func ollamaQuantFromName(modelName string) string {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return ""
	}
	if i := strings.LastIndex(modelName, ":"); i >= 0 && i < len(modelName)-1 {
		modelName = modelName[i+1:]
	}
	if m := ollamaQuantFromNameRe.FindStringSubmatch(modelName); len(m) > 1 {
		return strings.ToUpper(m[1])
	}
	if m := ollamaQuantLooseRe.FindString(modelName); m != "" {
		return strings.ToUpper(m)
	}
	return ""
}

// ollamaSameModel matches catalog tags with /api/ps names (digest vs :latest, etc.).
func ollamaSameModel(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return strings.HasPrefix(a, b+":") || strings.HasPrefix(b, a+":")
}

func (c *Client) llamacppRuntime(ctx context.Context, activeModel string, rescanDisk bool) (*RuntimeSnapshot, error) {
	out := &RuntimeSnapshot{Backend: "llamacpp", ActiveModel: activeModel}
	if rescanDisk {
		_, _ = c.get(ctx, "/models?reload=1")
	}
	b, fromOpenAIModels, err := c.listLlamaModels(ctx)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Data []struct {
			ID     string          `json:"id"`
			Size   int64           `json:"size"`
			Status json.RawMessage `json:"status"`
			Meta   json.RawMessage `json:"meta"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, err
	}

	for _, item := range resp.Data {
		if item.ID == "" {
			continue
		}
		st := parseLlamaStatus(item.Status)
		if fromOpenAIModels {
			// /v1/models 只返回当前加载的模型，不提供 Router 的 status 字段。
			st = llamaStatusParsed{display: "loaded", inMemory: true, running: true}
		}
		disk := item.Size
		if disk <= 0 {
			disk = parseLlamaMetaDisk(item.Meta)
		}
		if disk <= 0 {
			disk = parseLlamaStatusDisk(item.Status)
		}
		ci := CatalogItem{
			ID:        item.ID,
			Loaded:    st.inMemory,
			Status:    st.display,
			Active:    activeModel != "" && ModelIDMatch(item.ID, activeModel),
			Running:   st.running,
			DiskBytes: disk,
		}
		if st.vram > 0 {
			ci.VRAMBytes = st.vram
			ci.MemoryNote = "llama.cpp 上报"
		} else if st.ram > 0 {
			ci.RAMBytes = st.ram
			ci.MemoryNote = "llama.cpp 上报"
		} else if ci.Loaded {
			ci.MemoryNote = "已加载；显存请对照 GGUF 文件大小"
		}
		out.Catalog = append(out.Catalog, ci)
		if ci.Loaded {
			out.Loaded = append(out.Loaded, ci)
			out.LoadedVRAM += ci.VRAMBytes
			out.LoadedRAM += ci.RAMBytes
		}
	}

	if activeModel == "" && len(out.Loaded) > 0 {
		out.ActiveModel = out.Loaded[0].ID
	}
	return out, nil
}

type llamaStatusParsed struct {
	display  string
	inMemory bool
	running  bool
	vram     int64
	ram      int64
}

func parseLlamaStatus(raw json.RawMessage) llamaStatusParsed {
	if len(raw) == 0 || string(raw) == "null" {
		return llamaStatusParsed{display: "unloaded"}
	}
	value := llamaStatusValue(raw)
	p := llamaStatusParsed{display: value}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "loaded", "sleeping":
		p.inMemory = true
		p.running = true
	case "loading":
		p.running = true
	case "unloaded", "":
		p.display = "unloaded"
	default:
		p.display = value
	}
	var obj map[string]interface{}
	if json.Unmarshal(raw, &obj) == nil {
		p.vram = int64(jsonNumber(obj["vram_bytes"]))
		if p.vram == 0 {
			p.vram = int64(jsonNumber(obj["n_vram"]))
		}
		p.ram = int64(jsonNumber(obj["ram_bytes"]))
		if p.ram == 0 {
			p.ram = int64(jsonNumber(obj["n_ram"]))
		}
	}
	return p
}

func llamaStatusValue(raw json.RawMessage) string {
	var st struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &st) == nil && st.Value != "" {
		return st.Value
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	var obj map[string]interface{}
	if json.Unmarshal(raw, &obj) == nil {
		if v, ok := obj["value"].(string); ok {
			return v
		}
	}
	return ""
}

func parseLlamaMetaDisk(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var obj map[string]interface{}
	if json.Unmarshal(raw, &obj) != nil {
		return 0
	}
	return int64(jsonNumber(obj["size"]))
}

func parseLlamaStatusDisk(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var obj map[string]interface{}
	if json.Unmarshal(raw, &obj) != nil {
		return 0
	}
	prog, ok := obj["progress"].(map[string]interface{})
	if !ok {
		return 0
	}
	var max int64
	for _, v := range prog {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		total := int64(jsonNumber(m["total"]))
		if total > max {
			max = total
		}
	}
	return max
}

// ModelIDMatch returns true if a and b refer to the same llama router model id/alias.
func ModelIDMatch(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	if a == b || strings.EqualFold(a, b) {
		return true
	}
	astem := strings.TrimSuffix(filepath.Base(a), ".gguf")
	bstem := strings.TrimSuffix(filepath.Base(b), ".gguf")
	return astem != "" && astem == bstem
}

func jsonNumber(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		return 0
	}
}
