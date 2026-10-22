// downloader 从 Hugging Face 拉取 GGUF 模型文件，支持断点续传与 SHA256 校验。
package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HFDownloader 配置 HF 镜像地址、超时与本地 models 目录，下载过程可回调 progress。
type HFDownloader struct {
	base, ua  string
	http      *http.Client
	modelsDir string
	progress  func(string, int64, int64, string)
}

// NewHF 构造下载器；progress 参数会在 server 里写入 Redis 供前端展示进度。
func NewHF(base, ua string, timeout time.Duration, modelsDir string, progress func(string, int64, int64, string)) *HFDownloader {
	return &HFDownloader{base: strings.TrimRight(base, "/"), ua: ua, http: &http.Client{Timeout: timeout}, modelsDir: modelsDir, progress: progress}
}

type Entry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	LFS  *struct {
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	} `json:"lfs"`
}

func (e Entry) SizeBytes() int64 {
	if e.Size > 0 {
		return e.Size
	}
	if e.LFS != nil {
		return e.LFS.Size
	}
	return 0
}
func (e Entry) SHA256() string {
	if e.LFS != nil {
		return e.LFS.SHA256
	}
	return ""
}
// ListGGUF 列出仓库某分支下所有 .gguf 文件及大小，供安装向导选择。
func (d *HFDownloader) ListGGUF(ctx context.Context, repo, revision string) ([]Entry, error) {
	if revision == "" {
		revision = "main"
	}
	u := d.base + "/api/models/" + escapeRepo(repo) + "/tree/" + url.PathEscape(revision) + "?recursive=true&expand=false"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", d.ua)
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("hf tree: %s", strings.TrimSpace(string(b)))
	}
	var files []Entry
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(files))
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f.Path), ".gguf") {
			out = append(out, f)
		}
	}
	return out, nil
}
// Download 下载单个 GGUF 到 modelsDir，先写 .part 再校验哈希并 rename，返回字节数与 SHA256。
func (d *HFDownloader) Download(ctx context.Context, jobID, repo, revision, filename string) (int64, string, string, error) {
	if revision == "" {
		revision = "main"
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".gguf") {
		return 0, "", "", fmt.Errorf("only GGUF files are allowed")
	}
	entries, err := d.ListGGUF(ctx, repo, revision)
	if err != nil {
		return 0, "", "", err
	}
	var total int64
	var expected string
	found := false
	for _, e := range entries {
		if e.Path == filename {
			total = e.SizeBytes()
			expected = e.SHA256()
			found = true
			break
		}
	}
	if !found {
		return 0, "", "", fmt.Errorf("file %s not found in %s", filename, repo)
	}
	if err := os.MkdirAll(d.modelsDir, 0755); err != nil {
		return 0, "", "", err
	}
	final := filepath.Join(d.modelsDir, filepath.Base(filename))
	part := final + ".part"
	var offset int64
	if st, e := os.Stat(part); e == nil {
		offset = st.Size()
	}
	if total > 0 && offset > total {
		_ = os.Remove(part)
		offset = 0
	}
	u := d.base + "/" + escapeRepo(repo) + "/resolve/" + url.PathEscape(revision) + "/" + url.PathEscape(filename)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, "", "", err
	}
	req.Header.Set("User-Agent", d.ua)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	if offset > 0 && resp.StatusCode == http.StatusOK {
		// Server ignored Range; restart this download safely instead of appending a second copy.
		resp.Body.Close()
		offset = 0
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return 0, "", "", err
		}
		req.Header.Set("User-Agent", d.ua)
		resp, err = d.http.Do(req)
		if err != nil {
			return 0, "", "", err
		}
	}
	if offset > 0 && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return 0, "", "", fmt.Errorf("resume not supported: %s", resp.Status)
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return 0, "", "", fmt.Errorf("hf download: %s", strings.TrimSpace(string(b)))
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0644)
	if err != nil {
		resp.Body.Close()
		return 0, "", "", err
	}
	buf := make([]byte, 1024*1024)
	for {
		n, re := resp.Body.Read(buf)
		if n > 0 {
			if _, we := f.Write(buf[:n]); we != nil {
				f.Close()
				resp.Body.Close()
				return 0, "", "", we
			}
			offset += int64(n)
			if d.progress != nil {
				d.progress(jobID, offset, total, "downloading")
			}
		}
		if re == io.EOF {
			break
		}
		if re != nil {
			f.Close()
			resp.Body.Close()
			return 0, "", "", re
		}
		if err := ctx.Err(); err != nil {
			f.Close()
			resp.Body.Close()
			return 0, "", "", err
		}
	}
	_ = f.Close()
	resp.Body.Close()
	if total > 0 && offset != total {
		return 0, "", "", fmt.Errorf("download size mismatch: got %d expected %d", offset, total)
	}
	f, err = os.Open(part)
	if err != nil {
		return 0, "", "", err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	_ = f.Close()
	if err != nil {
		return 0, "", "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if expected != "" && !strings.EqualFold(expected, sum) {
		return 0, "", "", fmt.Errorf("sha256 mismatch: expected %s got %s", expected, sum)
	}
	if err = os.Rename(part, final); err != nil {
		return 0, "", "", err
	}
	if d.progress != nil {
		d.progress(jobID, offset, total, "completed")
	}
	return offset, sum, expected, nil
}
func escapeRepo(repo string) string {
	parts := strings.Split(strings.Trim(repo, "/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
