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

 
func TestDeleteLlamaDiskModelFilesDeletesAllShardsOnly(t *testing.T) {
	root := t.TempDir()
	files := map[string][]byte{
		"qwen.gguf":                           []byte("single"),
		"multi/model-00001-of-00002.gguf":     []byte("part-one"),
		"multi/model-00002-of-00002.gguf":     []byte("part-two"),
		"vision/mmproj-F16.gguf":              []byte("projector"),
		"multi/other-00001-of-00002.gguf":     []byte("unrelated-part"),
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}

	deleted, err := deleteLlamaDiskModelFiles(root, "multi/model.gguf")
	if err != nil {
		t.Fatalf("delete model shards: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted=%v, want two shard files", deleted)
	}
	for _, name := range []string{"multi/model-00001-of-00002.gguf", "multi/model-00002-of-00002.gguf"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Fatalf("shard %s still exists or stat failed: %v", name, err)
		}
	}
	for _, name := range []string{"qwen.gguf", "vision/mmproj-F16.gguf", "multi/other-00001-of-00002.gguf"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Fatalf("unrelated file %s was affected: %v", name, err)
		}
	}
}

func TestLlamaDiskModelFilesRejectsTraversalAndSymlink(t *testing.T) {
	root := t.TempDir()
	other := filepath.Join(t.TempDir(), "secret.gguf")
	if err := os.WriteFile(other, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := llamaDiskModelFiles(root, "../secret.gguf"); err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
	link := filepath.Join(root, "linked.gguf")
	if err := os.Symlink(other, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := llamaDiskModelFiles(root, "linked.gguf"); !os.IsNotExist(err) {
		t.Fatalf("symlink should not be treated as a model file; err=%v", err)
	}
}
