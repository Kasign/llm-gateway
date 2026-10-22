package hf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Row is one HF search hit before normalization to catalog.Listing.
type Row struct {
	ID          string
	DisplayName string
	Downloads   int64
	Likes       int64
	PublishedAt time.Time
	Tags        []string
	PipelineTag string
}

// Client searches Hugging Face model hub (read-only).
type Client struct {
	BaseURL    string
	UserAgent  string
	HTTPClient *http.Client
}

type apiModel struct {
	ID           string     `json:"id"`
	ModelID      string     `json:"modelId"`
	Likes        int64      `json:"likes"`
	Downloads    int64      `json:"downloads"`
	Tags         []string   `json:"tags"`
	PipelineTag  string     `json:"pipeline_tag"`
	LastModified *time.Time `json:"lastModified"`
	CreatedAt    time.Time  `json:"createdAt"`
}

// Search lists models matching q on the configured HF mirror/base URL.
func (c *Client) Search(ctx context.Context, q string, sortNewest bool, ggufOnly bool, limit int) ([]Row, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = "https://huggingface.co"
	}
	if limit <= 0 {
		limit = 30
	}
	if limit > 50 {
		limit = 50
	}
	sortField := "downloads"
	if sortNewest {
		sortField = "lastModified"
	}
	// 默认列表响应经常省略 lastModified / pipeline_tag，full=true 才会带回。
	u := base + "/api/models?" + url.Values{
		"search":    {strings.TrimSpace(q)},
		"sort":      {sortField},
		"direction": {"-1"},
		"limit":     {fmt.Sprintf("%d", limit)},
		"full":      {"true"},
	}.Encode()
	if ggufOnly {
		u += "&filter=gguf"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
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
		return nil, fmt.Errorf("hf search: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var rows []apiModel
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(rows))
	for _, m := range rows {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			id = strings.TrimSpace(m.ModelID)
		}
		if id == "" {
			continue
		}
		pub := m.CreatedAt
		if m.LastModified != nil && !m.LastModified.IsZero() {
			pub = *m.LastModified
		}
		item := Row{
			ID:          id,
			DisplayName: id,
			Downloads:   m.Downloads,
			Likes:       m.Likes,
			PublishedAt: pub,
			Tags:        append([]string(nil), m.Tags...),
			PipelineTag: m.PipelineTag,
		}
		out = append(out, item)
	}
	return out, nil
}
