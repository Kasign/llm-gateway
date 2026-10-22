package ollama

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// Tag is one pullable variant (model:tag).
type Tag struct {
	Tag      string `json:"tag"`
	FullName string `json:"full_name"`
	Size     string `json:"size,omitempty"`
	Context  string `json:"context,omitempty"`
	Input    string `json:"input,omitempty"`
}

var (
	reLibraryTagLink = regexp.MustCompile(`href="/([^"]+):([a-zA-Z0-9._-]+)"`)
	reTagGridRow     = regexp.MustCompile(`(?s)href="/([^"]+):([a-zA-Z0-9._-]+)"[^>]*>.*?<(?:p|div)\s+class="col-span-2 text-neutral-500[^"]*">\s*([^<]*?)\s*</(?:p|div)>\s*<(?:p|div)\s+class="col-span-2 text-neutral-500[^"]*">\s*([^<]*?)\s*</(?:p|div)>\s*<(?:p|div)\s+class="col-span-2 text-neutral-500[^"]*">\s*([^<]*?)\s*</(?:p|div)>`)
	reTagMobileMeta  = regexp.MustCompile(`(?s)href="/([^"]+):([a-zA-Z0-9._-]+)" class="sm:hidden[^"]*"[^>]*>.*?<p class="flex text-neutral-500">([^<]+)</p>`)
)

func parseOllamaMobileMeta(line string) (size, context, input string) {
	parts := strings.Split(line, "·")
	if len(parts) >= 1 {
		size = strings.TrimSpace(parts[0])
	}
	if len(parts) >= 2 {
		ctx := strings.TrimSpace(parts[1])
		ctx = strings.TrimSuffix(ctx, " context window")
		context = strings.TrimSpace(ctx)
	}
	if len(parts) >= 3 {
		input = strings.TrimSpace(parts[2])
	}
	return size, context, input
}

func parseLibraryTagsFromHTML(html, model string) []Tag {
	byTag := map[string]Tag{}
	add := func(tag Tag) {
		tag.Tag = strings.TrimSpace(tag.Tag)
		if tag.Tag == "" {
			return
		}
		tag.FullName = model + ":" + tag.Tag
		if prev, ok := byTag[tag.Tag]; ok {
			if tag.Size == "" {
				tag.Size = prev.Size
			}
			if tag.Context == "" {
				tag.Context = prev.Context
			}
			if tag.Input == "" {
				tag.Input = prev.Input
			}
		}
		byTag[tag.Tag] = tag
	}
	for _, m := range reTagGridRow.FindAllStringSubmatch(html, -1) {
		if len(m) < 6 || canonicalModelID(m[1]) != model {
			continue
		}
		add(Tag{
			Tag:     m[2],
			Size:    strings.TrimSpace(m[3]),
			Context: strings.TrimSpace(m[4]),
			Input:   strings.TrimSpace(m[5]),
		})
	}
	for _, m := range reTagMobileMeta.FindAllStringSubmatch(html, -1) {
		if len(m) < 4 || canonicalModelID(m[1]) != model {
			continue
		}
		size, ctx, input := parseOllamaMobileMeta(m[3])
		add(Tag{Tag: m[2], Size: size, Context: ctx, Input: input})
	}
	for _, m := range reLibraryTagLink.FindAllStringSubmatch(html, -1) {
		if len(m) < 3 || canonicalModelID(m[1]) != model {
			continue
		}
		tag := strings.TrimSpace(m[2])
		if tag == "" {
			continue
		}
		if _, ok := byTag[tag]; ok {
			continue
		}
		add(Tag{Tag: tag})
	}
	out := make([]Tag, 0, len(byTag))
	for _, t := range byTag {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tag == "latest" {
			return true
		}
		if out[j].Tag == "latest" {
			return false
		}
		return out[i].Tag < out[j].Tag
	})
	return out
}

func tagsPageURL(base, model string) string {
	model = strings.Trim(strings.TrimSpace(model), "/")
	if strings.Contains(model, "/") {
		return base + "/" + model + "/tags"
	}
	return base + "/library/" + model + "/tags"
}

// ListTags scrapes tag names from the public library page for a base model.
func (c *Client) ListTags(ctx context.Context, model string) ([]Tag, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, fmt.Errorf("model name required")
	}
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = "https://ollama.com"
	}
	pageURL := tagsPageURL(base, model)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
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
		return nil, fmt.Errorf("ollama library tags: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	html := string(body)
	out := parseLibraryTagsFromHTML(html, model)
	if len(out) == 0 {
		out = []Tag{{Tag: "latest", FullName: model + ":latest"}}
	}
	return out, nil
}
