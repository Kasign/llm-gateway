// model（disk）：网关模型磁盘大小与 upstream id 别名。
// 大小以磁盘文件为准；DB size_bytes 仅为缓存，在下载完成/启动对账时写入，列表 API 不扫盘。
package model

import (
	"os"
	"path/filepath"
	"strings"
)

// CachedDiskBytes 返回 DB 中缓存的 size_bytes（展示/API 热路径用，不访问磁盘）。
func CachedDiskBytes(m *Model) int64 {
	if m == nil || m.SizeBytes <= 0 {
		return 0
	}
	return m.SizeBytes
}

// StatDiskBytesFromFile 从 local_path stat 实际文件大小（仅在对账、下载完成等冷路径调用）。
func StatDiskBytesFromFile(m *Model) int64 {
	if m == nil {
		return 0
	}
	if p := strings.TrimSpace(m.LocalPath); p != "" {
		if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
			return fi.Size()
		}
	}
	return 0
}

// MatchKeys 返回可与上游 catalog id 比对的别名集合。
func MatchKeys(m *Model) []string {
	if m == nil {
		return nil
	}
	var keys []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		keys = append(keys, v, strings.TrimSuffix(v, ".gguf"))
	}
	add(m.ModelKey)
	add(m.LlamaModelName)
	add(m.Filename)
	if m.LocalPath != "" {
		add(m.LocalPath)
		add(filepath.Base(m.LocalPath))
	}
	if m.LlamaModelName != "" {
		add(filepath.Base(m.LlamaModelName))
	}
	return keys
}
