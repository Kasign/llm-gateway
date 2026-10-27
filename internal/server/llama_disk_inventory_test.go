package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanLlamaDiskInventoryMergesShardFilesAndSkipsProjectors(t *testing.T) {
	root := t.TempDir()
	files := map[string]int64{
		"qwen.gguf": 100,
		"vision/mmproj-F16.gguf": 10,
		"multi/model-00001-of-00002.gguf": 200,
		"multi/model-00002-of-00002.gguf": 300,
	}
	for name, size := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil { t.Fatal(err) }
		if err := os.WriteFile(path, make([]byte, size), 0600); err != nil { t.Fatal(err) }
	}
	items, err := scanLlamaDiskInventory(root)
	if err != nil { t.Fatalf("scan inventory: %v", err) }
	if len(items) != 2 { t.Fatalf("items=%#v, want two logical models", items) }
	foundQwen, foundMulti := false, false
	for _, item := range items {
		switch item.DisplayName {
		case "qwen.gguf":
			foundQwen = true
			if item.DiskBytes != 100 || !item.DiskPresent || item.Registered { t.Fatalf("qwen item=%+v", item) }
		case "multi/model.gguf":
			foundMulti = true
			if item.DiskBytes != 500 || filepath.Base(item.Path) != "model-00001-of-00002.gguf" { t.Fatalf("multi item=%+v", item) }
		}
	}
	if !foundQwen || !foundMulti { t.Fatalf("inventory missing expected models: %#v", items) }
}
