package catalog

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/whl/llm-gateway/internal/catalog/hf"
	"github.com/whl/llm-gateway/internal/catalog/ollama"
)

// Config wires catalog search to deployment settings.
type Config struct {
	HFBaseURL          string
	HFUserAgent        string
	OllamaLibraryURL   string
	HTTPTimeout        time.Duration
}

// Service runs model discovery across sources.
type Service struct {
	cfg Config
	hc  *http.Client
}

// NewService builds a catalog service.
func NewService(cfg Config) *Service {
	to := cfg.HTTPTimeout
	if to <= 0 {
		to = 60 * time.Second
	}
	return &Service{
		cfg: cfg,
		hc:  &http.Client{Timeout: to},
	}
}

// Search queries one or both sources and merges results.
func (s *Service) Search(ctx context.Context, q Query) ([]Listing, error) {
	q.Text = strings.TrimSpace(q.Text)
	if q.Limit <= 0 {
		q.Limit = 30
	}
	if q.Limit > 50 {
		q.Limit = 50
	}
	if q.Sort == "" {
		q.Sort = SortHot
	}
	src := strings.ToLower(strings.TrimSpace(q.Source))
	if src == "" || src == "all" {
		src = "all"
	}

	var hfRows, ollamaRows []Listing
	var errHF, errOllama error

	newest := q.Sort == SortNewest
	if src == "all" || src == "hf" || src == "huggingface" {
		hfc := &hf.Client{BaseURL: s.cfg.HFBaseURL, UserAgent: s.cfg.HFUserAgent, HTTPClient: s.hc}
		ggufOnly := q.HFGGUFOnly
		raw, err := hfc.Search(ctx, q.Text, newest, ggufOnly, q.Limit)
		errHF = err
		for _, r := range raw {
			hfRows = append(hfRows, listingFromHF(r))
		}
	}
	if src == "all" || src == "ollama" {
		oc := &ollama.Client{BaseURL: s.cfg.OllamaLibraryURL, UserAgent: s.cfg.HFUserAgent, HTTPClient: s.hc}
		raw, err := oc.Search(ctx, q.Text, newest, q.Limit)
		errOllama = err
		for _, r := range raw {
			ollamaRows = append(ollamaRows, listingFromOllama(r))
		}
	}

	if errHF != nil && errOllama != nil {
		return nil, errHF
	}
	if src == "hf" || src == "huggingface" {
		return hfRows, errHF
	}
	if src == "ollama" {
		return ollamaRows, errOllama
	}
	merged := MergeSorted(hfRows, ollamaRows, q.Sort, q.Limit)
	if errHF != nil && len(hfRows) == 0 {
		return merged, nil
	}
	if errOllama != nil && len(ollamaRows) == 0 {
		return merged, nil
	}
	return merged, nil
}
