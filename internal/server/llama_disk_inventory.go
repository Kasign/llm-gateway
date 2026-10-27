package server

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/whl/llm-gateway/internal/llama"
	"github.com/whl/llm-gateway/internal/model"
)

var llamaShardSuffix = regexp.MustCompile(`(?i)-([0-9]{5})-of-([0-9]{5})\.gguf$`)

type llamaDiskGroup struct { ID, DisplayName, Path string; SizeBytes int64 }

// scanLlamaDiskInventory 只枚举文件元数据，不读取模型权重内容；多分片模型合并为一条逻辑记录。
func scanLlamaDiskInventory(root string) ([]llama.CatalogItem, error) {
	root = strings.TrimSpace(root)
	if root == "" { return nil, nil }
	root, err := filepath.Abs(root)
	if err != nil { return nil, err }
	info, err := os.Stat(root)
	if err != nil { return nil, err }
	if !info.IsDir() { return nil, os.ErrInvalid }
	groups := make(map[string]*llamaDiskGroup)
	var warning error
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if filepath.Clean(path) == filepath.Clean(root) { return walkErr }
			if warning == nil { warning = walkErr }
			return nil
		}
		if entry.IsDir() { return nil }
		name := filepath.Base(path)
		if !strings.EqualFold(filepath.Ext(name), ".gguf") || strings.HasPrefix(strings.ToLower(name), "mmproj") { return nil }
		fi, err := entry.Info()
		if err != nil { if warning == nil { warning = err }; return nil }
		rel, err := filepath.Rel(root, path)
		if err != nil { return err }
		rel = filepath.ToSlash(rel)
		key := rel
		if llamaShardSuffix.MatchString(name) { key = llamaShardSuffix.ReplaceAllString(rel, ".gguf") }
		group := groups[key]
		if group == nil { group = &llamaDiskGroup{ID:key, DisplayName:key}; groups[key] = group }
		group.SizeBytes += fi.Size()
		if group.Path == "" || llamaShardNumber(name) == "00001" { group.Path = path }
		return nil
	})
	if err != nil { return nil, err }
	keys := make([]string, 0, len(groups))
	for key := range groups { keys = append(keys, key) }
	sort.Strings(keys)
	out := make([]llama.CatalogItem, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		out = append(out, llama.CatalogItem{
			ID:group.ID, DisplayName:group.DisplayName, Path:group.Path, Status:"not_registered",
			DiskBytes:group.SizeBytes, DiskPresent:true, Source:"disk",
		})
	}
	return out, warning
}

func llamaShardNumber(name string) string {
	match := llamaShardSuffix.FindStringSubmatch(name)
	if len(match) != 3 { return "" }
	return match[1]
}

func catalogPathsMatch(a, b string) bool {
	a = strings.ToLower(filepath.ToSlash(filepath.Clean(strings.TrimSpace(a))))
	b = strings.ToLower(filepath.ToSlash(filepath.Clean(strings.TrimSpace(b))))
	return a != "" && b != "" && (a == b || strings.HasSuffix(a, "/"+b) || strings.HasSuffix(b, "/"+a))
}

func llamaCatalogItemsMatch(a, b llama.CatalogItem) bool {
	if catalogPathsMatch(a.Path, b.Path) { return true }
	if a.ID != "" && b.ID != "" && strings.EqualFold(a.ID, b.ID) { return true }
	if a.Path != "" && b.Path != "" && filepath.IsAbs(a.Path) && filepath.IsAbs(b.Path) { return false }
	if llama.ModelIDMatch(a.ID, b.ID) { return true }
	if a.Path != "" && llama.ModelIDMatch(filepath.Base(a.Path), b.ID) { return true }
	if b.Path != "" && llama.ModelIDMatch(filepath.Base(b.Path), a.ID) { return true }
	return a.DisplayName != "" && b.DisplayName != "" && llama.ModelIDMatch(a.DisplayName, b.DisplayName)
}

func mergeCatalogSource(current, incoming string) string {
	seen := make(map[string]bool)
	var parts []string
	for _, raw := range []string{current, incoming} {
		for _, part := range strings.Split(raw, "+") {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] { continue }
			seen[part] = true
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "+")
}

func gatewayCatalogItem(m model.Model) llama.CatalogItem {
	id := strings.TrimSpace(strings.TrimSuffix(m.LlamaModelName, ".gguf"))
	if id == "" { id = strings.TrimSpace(strings.TrimSuffix(m.Filename, ".gguf")) }
	if id == "" { id = strings.TrimSpace(m.ModelKey) }
	display := strings.TrimSpace(m.DisplayName)
	if display == "" { display = strings.TrimSpace(m.Filename) }
	status := "unknown"
	switch strings.ToLower(strings.TrimSpace(m.Status)) {
	case "downloading", "download_failed", "download_cancelled":
		status = strings.ToLower(strings.TrimSpace(m.Status))
	}
	item := llama.CatalogItem{
		ID:id, DisplayName:display, Path:strings.TrimSpace(m.LocalPath), Status:status,
		DiskBytes:model.CachedDiskBytes(&m), Source:"gateway",
	}
	if item.Path != "" {
		if fi, err := os.Stat(item.Path); err == nil && !fi.IsDir() {
			item.DiskPresent = true
			if fi.Size() > 0 { item.DiskBytes = fi.Size() }
		}
	}
	return item
}

// mergeLlamacppCatalog 以 Router 实时状态为准，合并磁盘 GGUF 与 Gateway 数据库记录。
func (s *Server) mergeLlamacppCatalog(snap *llama.RuntimeSnapshot, dbModels []model.Model, upstreamAvailable bool) string {
	if snap == nil { return "" }
	configured := strings.TrimSpace(s.activeModelHintForBackend("llamacpp"))
	for i := range snap.Catalog {
		item := &snap.Catalog[i]
		if item.Source == "" {
			if item.Registered { item.Source = "router" } else { item.Source = "upstream" }
		}
		if item.DisplayName == "" { item.DisplayName = filepath.Base(item.ID) }
		item.Active = configured != "" && llama.ModelIDMatch(item.ID, configured)
	}
	disks, scanErr := scanLlamaDiskInventory(s.llamaModelsDir())
	for _, disk := range disks {
		matched := -1
		for i := range snap.Catalog {
			if llamaCatalogItemsMatch(snap.Catalog[i], disk) { matched = i; break }
		}
		if matched < 0 {
			if !upstreamAvailable { disk.Status = "unknown" }
			disk.Active = configured != "" && llama.ModelIDMatch(disk.ID, configured)
			snap.Catalog = append(snap.Catalog, disk)
			continue
		}
		item := &snap.Catalog[matched]
		item.Source = mergeCatalogSource(item.Source, "disk")
		item.DiskPresent = true
		item.Path = disk.Path
		if disk.DisplayName != "" { item.DisplayName = disk.DisplayName }
		if disk.DiskBytes > 0 { item.DiskBytes = disk.DiskBytes }
		if !upstreamAvailable && !item.Loaded {
			item.Status = "unknown"
		} else if upstreamAvailable && !item.Registered && !item.Loaded && item.Status == "unknown" {
			item.Status = "not_registered"
		}
		item.Active = configured != "" && llama.ModelIDMatch(item.ID, configured)
	}
	for _, m := range dbModels {
		gw := gatewayCatalogItem(m)
		matched := -1
		for i := range snap.Catalog {
			item := snap.Catalog[i]
			ok := llamaCatalogItemsMatch(item, gw)
			if !ok {
				for _, key := range model.MatchKeys(&m) {
					if llama.ModelIDMatch(item.ID, key) || (item.Path != "" && llama.ModelIDMatch(filepath.Base(item.Path), key)) { ok = true; break }
				}
			}
			if ok { matched = i; break }
		}
		if matched < 0 {
			if upstreamAvailable && gw.DiskPresent { gw.Status = "not_registered" }
			gw.Active = configured != "" && llama.ModelIDMatch(gw.ID, configured)
			snap.Catalog = append(snap.Catalog, gw)
			continue
		}
		item := &snap.Catalog[matched]
		item.Source = mergeCatalogSource(item.Source, "gateway")
		if item.DisplayName == "" { item.DisplayName = gw.DisplayName }
		if item.Path == "" { item.Path = gw.Path }
		item.DiskPresent = item.DiskPresent || gw.DiskPresent
		if item.DiskBytes <= 0 { item.DiskBytes = gw.DiskBytes }
		if (item.Status == "" || item.Status == "unknown") && !item.Loaded {
			switch gw.Status {
			case "downloading", "download_failed", "download_cancelled": item.Status = gw.Status
			}
		}
		item.Active = configured != "" && llama.ModelIDMatch(item.ID, configured)
	}
	snap.Loaded = nil
	snap.LoadedVRAM, snap.LoadedRAM = 0, 0
	for _, item := range snap.Catalog {
		if !item.Loaded { continue }
		snap.Loaded = append(snap.Loaded, item)
		snap.LoadedVRAM += item.VRAMBytes
		snap.LoadedRAM += item.RAMBytes
	}
	if scanErr != nil { return "读取 Gateway GGUF 共享目录失败，部分磁盘模型可能未显示：" + scanErr.Error() }
	return ""
}
