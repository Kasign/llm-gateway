package ollama

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Row is one Ollama library card before normalization.
type Row struct {
	ID           string
	DisplayName  string
	Description  string
	Pulls        *int64
	UpdatedLabel string
	Tags         []string
	ParamTags    []string // e.g. 8b, 3b (library card pills, not file size)
	SortPulls    int64
}

// Client searches the public Ollama model library (HTML; no official JSON API).
type Client struct {
	BaseURL    string
	UserAgent  string
	HTTPClient *http.Client
}

var (
	// 官网搜索卡片的 class 已从 group w-full 改为 flex 布局；旧 class 仍保留给镜像页。
	reLibraryLink    = regexp.MustCompile(`(?s)<a href="/([^"]+)" class="group flex items-start[^"]*">(.*?)</a>`)
	reLibraryLinkOld = regexp.MustCompile(`(?s)<a href="/library/([^"]+)" class="group w-full">(.*?)</a>`)
	reTitle          = regexp.MustCompile(`<span[^>]*>([^<]+)</span>\s*</h2>`)
	reDesc           = regexp.MustCompile(`<p class="mt-1 max-w-2xl[^"]*"[^>]*>([^<]*)</p>`)
	reDescOld        = regexp.MustCompile(`<p class="max-w-lg break-words[^"]*"[^>]*>([^<]*)</p>`)
	rePullsExact     = regexp.MustCompile(`title="([0-9][0-9,]*) downloads"`)
	rePulls          = regexp.MustCompile(`<span[^>]*>\s*([0-9][^<]*)\s*</span>\s*<span class="hidden sm:flex">&nbsp;Pulls`)
	reUpdated        = regexp.MustCompile(`Updated&nbsp;</span>\s*<span[^>]*>\s*([^<]+)\s*</span>`)
	reTagPill        = regexp.MustCompile(`class="[^"]*text-indigo-600[^"]*"[^>]*>([^<]+)</span>`)
	reSizePill       = regexp.MustCompile(`class="[^"]*text-blue-600[^"]*"[^>]*>([^<]+)</span>`)
)

// Search queries ollama.com/search (or configured base) and parses listing cards.
func (c *Client) Search(ctx context.Context, q string, sortNewest bool, limit int) ([]Row, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = "https://ollama.com"
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > 50 {
		limit = 50
	}
	order := "popular"
	if sortNewest {
		order = "newest"
	}
	searchURL := base + "/search?" + url.Values{
		"q": {strings.TrimSpace(q)},
		"o": {order},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	req.Header.Set("Accept", "text/html")
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama library search: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	page := string(body)
	matches := reLibraryLink.FindAllStringSubmatch(page, -1)
	if len(matches) == 0 {
		matches = reLibraryLinkOld.FindAllStringSubmatch(page, -1)
	}
	out := make([]Row, 0, len(matches))
	seen := map[string]struct{}{}
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		name := canonicalModelID(m[1])
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		block := m[2]
		display := name
		if t := reTitle.FindStringSubmatch(block); len(t) > 1 {
			display = strings.TrimSpace(html.UnescapeString(t[1]))
		}
		desc := ""
		if d := reDesc.FindStringSubmatch(block); len(d) > 1 {
			desc = strings.TrimSpace(html.UnescapeString(d[1]))
		} else if d := reDescOld.FindStringSubmatch(block); len(d) > 1 {
			desc = strings.TrimSpace(html.UnescapeString(d[1]))
		}
		var pulls *int64
		var sortPulls int64
		if p := rePullsExact.FindStringSubmatch(block); len(p) > 1 {
			if n, ok := parseHumanCount(strings.TrimSpace(p[1])); ok {
				pulls = &n
				sortPulls = n
			}
		} else if p := rePulls.FindStringSubmatch(block); len(p) > 1 {
			if n, ok := parseHumanCount(strings.TrimSpace(p[1])); ok {
				pulls = &n
				sortPulls = n
			}
		}
		updated := ""
		if u := reUpdated.FindStringSubmatch(block); len(u) > 1 {
			updated = strings.TrimSpace(u[1])
		}
		var tags, paramTags []string
		for _, tg := range reTagPill.FindAllStringSubmatch(block, -1) {
			if len(tg) > 1 {
				tags = append(tags, strings.TrimSpace(tg[1]))
			}
		}
		for _, tg := range reSizePill.FindAllStringSubmatch(block, -1) {
			if len(tg) > 1 {
				paramTags = append(paramTags, strings.TrimSpace(tg[1]))
			}
		}
		item := Row{
			ID:           name,
			DisplayName:  display,
			Description:  desc,
			Pulls:        pulls,
			UpdatedLabel: updated,
			Tags:         tags,
			ParamTags:    paramTags,
			SortPulls:    sortPulls,
		}
		out = append(out, item)
		if len(out) >= limit {
			break
		}
	}
	sortOllamaRows(out, sortNewest)
	return out, nil
}

// canonicalModelID 去掉官网链接里的 library/ 前缀。用户模型保留 namespace/name。
func canonicalModelID(path string) string {
	path = strings.Trim(strings.TrimSpace(path), "/")
	return strings.TrimPrefix(path, "library/")
}

func sortOllamaRows(items []Row, newest bool) {
	if !newest {
		sort.SliceStable(items, func(i, j int) bool {
			return items[i].SortPulls > items[j].SortPulls
		})
	}
}

func parseHumanCount(s string) (int64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return 0, false
	}
	mult := float64(1)
	switch strings.ToUpper(s[len(s)-1:]) {
	case "K":
		mult = 1e3
		s = s[:len(s)-1]
	case "M":
		mult = 1e6
		s = s[:len(s)-1]
	case "B":
		mult = 1e9
		s = s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return int64(f * mult), true
}

// DefaultHTTPTimeout for library HTML fetches.
const DefaultHTTPTimeout = 60 * time.Second

// SearchURL builds the library search URL (for error messages).
func SearchURL(base, q string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = "https://ollama.com"
	}
	return base + "/search?" + url.Values{"q": {q}}.Encode()
}
