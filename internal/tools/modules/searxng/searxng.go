// Package searxng implements the web.search tool using a server-configured SearXNG endpoint.
package searxng

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/whl/llm-gateway/internal/tools"
)

const (
	DefaultRequestTimeout = 8 * time.Second
	MaxRequestTimeout     = 30 * time.Second
	DefaultMaxResults     = 5
	MaxResults            = 5
	DefaultMaxResponseBytes int64 = 512 * 1024
	MaxResponseBytes       int64 = 4 * 1024 * 1024
	MaxOutputBytes         = 16 * 1024
	MaxQueryRunes          = 512
)

var (
	ErrInvalidConfig       = errors.New("invalid SearXNG configuration")
	ErrInvalidArguments    = errors.New("invalid SearXNG arguments")
	ErrInvalidQuery        = errors.New("invalid SearXNG query")
	ErrUpstreamUnavailable = errors.New("SearXNG is unavailable")
	ErrUpstreamTimeout     = errors.New("SearXNG request timed out")
	ErrUpstreamStatus      = errors.New("SearXNG returned a non-success status")
	ErrResponseTooLarge    = errors.New("SearXNG response exceeds the configured size limit")
	ErrInvalidResponse     = errors.New("SearXNG returned an invalid response")
	ErrOutputTooLarge      = errors.New("normalized SearXNG result exceeds the output size limit")
	ErrBlockedAddress      = errors.New("SearXNG host resolves to a blocked network address")
)

type Config struct {
	BaseURL             string
	AllowInsecureHTTP   bool
	AllowPrivateNetwork bool
	RequestTimeout      time.Duration
	MaxResults          int
	MaxResponseBytes    int64
	SafeSearch          *int
	DefaultLanguage     string
	Categories          []string
}

type normalizedConfig struct {
	baseURL             *url.URL
	allowPrivateNetwork bool
	requestTimeout      time.Duration
	maxResults          int
	maxResponseBytes    int64
	safeSearch          int
	defaultLanguage     string
	categories          []string
}

type Executor struct {
	cfg    normalizedConfig
	client *http.Client
}

type StatusError struct {
	StatusCode int
}

func (e *StatusError) Error() string { return ErrUpstreamStatus.Error() }
func (e *StatusError) Is(target error) bool { return target == ErrUpstreamStatus }

type UpstreamError struct {
	cause error
}

func (e *UpstreamError) Error() string { return ErrUpstreamUnavailable.Error() }
func (e *UpstreamError) Unwrap() error {
	if e == nil { return nil }
	return e.cause
}
func (e *UpstreamError) Is(target error) bool { return target == ErrUpstreamUnavailable }

type ipResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type upstreamResponse struct {
	Results *[]upstreamResult `json:"results"`
}

type upstreamResult struct {
	Title         string          `json:"title"`
	URL           string          `json:"url"`
	Content       string          `json:"content"`
	Engine        string          `json:"engine"`
	Score         *float64        `json:"score"`
	PublishedDate json.RawMessage `json:"publishedDate"`
}

type searchOutput struct {
	Query   string         `json:"query"`
	Results []searchResult `json:"results"`
}

type searchResult struct {
	Title       string   `json:"title,omitempty"`
	URL         string   `json:"url"`
	Content     string   `json:"content,omitempty"`
	Engine      string   `json:"engine,omitempty"`
	Score       *float64 `json:"score,omitempty"`
	PublishedAt string   `json:"published_at,omitempty"`
}

// Definition 返回稳定的模型可见工具契约。运行配置不会进入工具名称或参数。
func Definition() tools.Definition {
	return tools.Definition{
		Name:        "web.search",
		Description: "Search the web using the configured SearXNG service and return a small set of relevant results.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":512}},"required":["query"],"additionalProperties":false}`),
	}
}

// New 构造 SearXNG Executor，并创建禁止代理、校验 DNS 目标且不跟随重定向的 HTTP Client。
func New(config Config) (*Executor, error) {
	cfg, err := normalizeConfig(config)
	if err != nil { return nil, err }
	client := newSafeHTTPClient(cfg, net.DefaultResolver)
	return &Executor{cfg: cfg, client: client}, nil
}

func normalizeConfig(config Config) (normalizedConfig, error) {
	raw := strings.TrimSpace(config.BaseURL)
	if raw == "" {
		return normalizedConfig{}, fmt.Errorf("%w: base_url is required", ErrInvalidConfig)
	}
	base, err := url.Parse(raw)
	if err != nil || base == nil || !base.IsAbs() || base.Hostname() == "" ||
		base.User != nil || base.RawQuery != "" || base.ForceQuery ||
		base.Fragment != "" || base.RawFragment != "" || base.Opaque != "" ||
		strings.Contains(base.Host, "\\") || strings.Contains(base.Hostname(), "%") {
		return normalizedConfig{}, fmt.Errorf("%w: base_url must be an absolute HTTP(S) URL without userinfo, query, or fragment", ErrInvalidConfig)
	}
	base.Scheme = strings.ToLower(base.Scheme)
	if base.Scheme != "http" && base.Scheme != "https" {
		return normalizedConfig{}, fmt.Errorf("%w: base_url scheme must be HTTP or HTTPS", ErrInvalidConfig)
	}
	if base.Scheme == "http" && !config.AllowInsecureHTTP {
		return normalizedConfig{}, fmt.Errorf("%w: plain HTTP requires allow_insecure_http", ErrInvalidConfig)
	}
	if strings.Contains(base.Path, "\\") {
		return normalizedConfig{}, fmt.Errorf("%w: base_url path is invalid", ErrInvalidConfig)
	}
	for _, segment := range strings.Split(base.Path, "/") {
		if segment == "." || segment == ".." {
			return normalizedConfig{}, fmt.Errorf("%w: base_url must not contain dot segments", ErrInvalidConfig)
		}
	}
	base.Path = strings.TrimRight(base.Path, "/")
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""

	if config.RequestTimeout == 0 { config.RequestTimeout = DefaultRequestTimeout }
	if config.RequestTimeout < time.Millisecond || config.RequestTimeout > MaxRequestTimeout {
		return normalizedConfig{}, fmt.Errorf("%w: request_timeout must be between 1ms and %s", ErrInvalidConfig, MaxRequestTimeout)
	}
	if config.MaxResults == 0 { config.MaxResults = DefaultMaxResults }
	if config.MaxResults < 1 || config.MaxResults > MaxResults {
		return normalizedConfig{}, fmt.Errorf("%w: max_results must be between 1 and %d", ErrInvalidConfig, MaxResults)
	}
	if config.MaxResponseBytes == 0 { config.MaxResponseBytes = DefaultMaxResponseBytes }
	if config.MaxResponseBytes < 1 || config.MaxResponseBytes > MaxResponseBytes {
		return normalizedConfig{}, fmt.Errorf("%w: max_response_bytes must be between 1 and %d", ErrInvalidConfig, MaxResponseBytes)
	}
	safeSearch := 1
	if config.SafeSearch != nil { safeSearch = *config.SafeSearch }
	if safeSearch < 0 || safeSearch > 2 {
		return normalizedConfig{}, fmt.Errorf("%w: safe_search must be 0, 1, or 2", ErrInvalidConfig)
	}
	language := strings.TrimSpace(config.DefaultLanguage)
	if language != "" {
		if len(language) > 16 {
			return normalizedConfig{}, fmt.Errorf("%w: default_language is too long", ErrInvalidConfig)
		}
		for _, r := range language {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return normalizedConfig{}, fmt.Errorf("%w: default_language contains unsupported characters", ErrInvalidConfig)
			}
		}
	}
	categories := make([]string, 0, len(config.Categories))
	if len(config.Categories) == 0 {
		categories = append(categories, "general")
	} else {
		for _, category := range config.Categories {
			category = strings.TrimSpace(category)
			if category == "" || strings.ContainsAny(category, ",\r\n") {
				return normalizedConfig{}, fmt.Errorf("%w: categories must be non-empty names without commas or line breaks", ErrInvalidConfig)
			}
			categories = append(categories, category)
		}
	}
	return normalizedConfig{
		baseURL: base, allowPrivateNetwork: config.AllowPrivateNetwork,
		requestTimeout: config.RequestTimeout, maxResults: config.MaxResults,
		maxResponseBytes: config.MaxResponseBytes, safeSearch: safeSearch,
		defaultLanguage: language, categories: categories,
	}, nil
}

func newSafeHTTPClient(cfg normalizedConfig, resolver ipResolver) *http.Client {
	if resolver == nil { resolver = net.DefaultResolver }
	dialer := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 3 * time.Second,
		ResponseHeaderTimeout: cfg.requestTimeout,
		IdleConnTimeout: 30 * time.Second,
		MaxIdleConns: 8,
		MaxIdleConnsPerHost: 4,
		MaxConnsPerHost: 8,
		ExpectContinueTimeout: time.Second,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil { return nil, err }
			var addresses []netip.Addr
			if ip, parseErr := netip.ParseAddr(strings.Trim(host, "[]")); parseErr == nil {
				addresses = []netip.Addr{ip.Unmap()}
			} else {
				lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
				addresses, err = resolver.LookupNetIP(lookupCtx, "ip", host)
				cancel()
				if err != nil { return nil, err }
			}
			var lastErr error
			blocked := 0
			for _, addr := range addresses {
				addr = addr.Unmap()
				if !allowedSearXNGAddress(addr, cfg.allowPrivateNetwork) {
					blocked++
					continue
				}
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
				if dialErr == nil { return conn, nil }
				lastErr = dialErr
			}
			if len(addresses) > 0 && blocked == len(addresses) { return nil, ErrBlockedAddress }
			if lastErr != nil { return nil, lastErr }
			return nil, errors.New("SearXNG host has no usable addresses")
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout: cfg.requestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}

var blockedSearXNGPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
}

func allowedSearXNGAddress(addr netip.Addr, allowPrivate bool) bool {
	if !addr.IsValid() { return false }
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsLoopback() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range blockedSearXNGPrefixes {
		if prefix.Contains(addr) { return false }
	}
	if addr.IsPrivate() { return allowPrivate }
	return true
}

// newWithClient is package-private and used by protocol tests; production uses New and the restricted client above.
func newWithClient(config Config, client *http.Client) (*Executor, error) {
	cfg, err := normalizeConfig(config)
	if err != nil { return nil, err }
	if client == nil { return nil, fmt.Errorf("%w: HTTP client is nil", ErrInvalidConfig) }
	clientCopy := *client
	clientCopy.Timeout = cfg.requestTimeout
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Executor{cfg: cfg, client: &clientCopy}, nil
}

func (e *Executor) Execute(ctx context.Context, arguments json.RawMessage) (json.RawMessage, error) {
	if e == nil || e.client == nil || e.cfg.baseURL == nil {
		return nil, errors.New("SearXNG executor is unavailable")
	}
	if ctx == nil { return nil, ErrInvalidArguments }
	var input struct { Query string `json:"query"` }
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil { return nil, ErrInvalidArguments }
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF { return nil, ErrInvalidArguments }
	query := strings.TrimSpace(input.Query)
	if query == "" || utf8.RuneCountInString(query) > MaxQueryRunes {
		return nil, ErrInvalidQuery
	}

	requestURL := *e.cfg.baseURL
	requestURL.Path = strings.TrimRight(requestURL.Path, "/") + "/search"
	values := requestURL.Query()
	values.Set("q", query)
	values.Set("format", "json")
	values.Set("safesearch", strconv.Itoa(e.cfg.safeSearch))
	if e.cfg.defaultLanguage != "" { values.Set("language", e.cfg.defaultLanguage) }
	if len(e.cfg.categories) > 0 { values.Set("categories", strings.Join(e.cfg.categories, ",")) }
	requestURL.RawQuery = values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil { return nil, ErrInvalidConfig }
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "llm-gateway/3.0")
	resp, err := e.client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil { return nil, ctxErr }
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %w", ErrUpstreamTimeout, err)
		}
		return nil, &UpstreamError{cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &StatusError{StatusCode: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, e.cfg.maxResponseBytes+1))
	if err != nil { return nil, &UpstreamError{cause: err} }
	if int64(len(raw)) > e.cfg.maxResponseBytes { return nil, ErrResponseTooLarge }
	if !utf8.Valid(raw) || !json.Valid(raw) { return nil, ErrInvalidResponse }
	var upstream upstreamResponse
	if err := json.Unmarshal(raw, &upstream); err != nil || upstream.Results == nil {
		return nil, ErrInvalidResponse
	}
	results := make([]searchResult, 0, e.cfg.maxResults)
	for _, item := range *upstream.Results {
		if len(results) >= e.cfg.maxResults { break }
		resultURL := normalizeResultURL(item.URL)
		if resultURL == "" { continue }
		title := clipUTF8(strings.TrimSpace(item.Title), 256)
		content := clipUTF8(strings.TrimSpace(item.Content), 512)
		if title == "" && content == "" { continue }
		result := searchResult{
			Title: title, URL: resultURL, Content: content,
			Engine: clipUTF8(strings.TrimSpace(item.Engine), 64), Score: item.Score,
		}
		if len(item.PublishedDate) > 0 && string(item.PublishedDate) != "null" {
			var published string
			if json.Unmarshal(item.PublishedDate, &published) == nil {
				published = clipUTF8(strings.TrimSpace(published), 64)
				if published != "" { result.PublishedAt = published }
			}
		}
		results = append(results, result)
	}
	output, err := json.Marshal(searchOutput{Query: query, Results: results})
	if err != nil { return nil, ErrInvalidResponse }
	if len(output) > MaxOutputBytes { return nil, ErrOutputTooLarge }
	return output, nil
}

func normalizeResultURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u == nil || !u.IsAbs() || u.Hostname() == "" || u.User != nil {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" { return "" }
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

func clipUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 { return "" }
	if len(value) <= maxBytes { return value }
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) { end-- }
	return value[:end]
}
