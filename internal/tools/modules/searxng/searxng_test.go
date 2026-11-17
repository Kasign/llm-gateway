package searxng

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/whl/llm-gateway/internal/tools"
)

func testConfig(baseURL string) Config {
	return Config{
		BaseURL: baseURL, AllowInsecureHTTP: true, AllowPrivateNetwork: true,
		RequestTimeout: 2 * time.Second, MaxResults: 5,
		MaxResponseBytes: 64 * 1024,
	}
}

func testExecutor(t *testing.T, server *httptest.Server, cfg Config) *Executor {
	t.Helper()
	if cfg.BaseURL == "" { cfg.BaseURL = server.URL }
	executor, err := newWithClient(cfg, &http.Client{})
	if err != nil { t.Fatal(err) }
	return executor
}

func TestDefinitionMatchesStableToolContract(t *testing.T) {
	def := Definition()
	if def.Name != "web.search" { t.Fatalf("tool name = %q", def.Name) }
	if err := tools.ValidateSchema(def.Parameters); err != nil { t.Fatalf("invalid tool schema: %v", err) }
}

func TestNewConfigDefaultsAndValidation(t *testing.T) {
	cfg := testConfig("https://search.example")
	cfg.RequestTimeout = 0
	cfg.MaxResults = 0
	cfg.MaxResponseBytes = 0
	cfg.SafeSearch = nil
	cfg.Categories = nil
	executor, err := New(cfg)
	if err != nil { t.Fatal(err) }
	if executor.cfg.requestTimeout != DefaultRequestTimeout { t.Fatalf("timeout = %s", executor.cfg.requestTimeout) }
	if executor.cfg.maxResults != DefaultMaxResults { t.Fatalf("max results = %d", executor.cfg.maxResults) }
	if executor.cfg.maxResponseBytes != DefaultMaxResponseBytes { t.Fatalf("max response = %d", executor.cfg.maxResponseBytes) }
	if executor.cfg.safeSearch != 1 { t.Fatalf("safe search = %d", executor.cfg.safeSearch) }
	if len(executor.cfg.categories) != 1 || executor.cfg.categories[0] != "general" { t.Fatalf("categories = %#v", executor.cfg.categories) }

	invalid := []Config{
		{BaseURL: "http://search.example"},
		{BaseURL: "ftp://search.example"},
		{BaseURL: "https://user:pass@search.example"},
		{BaseURL: "https://search.example/?token=x"},
		{BaseURL: "https://search.example/#fragment"},
		{BaseURL: "https://search.example/a/../b"},
		{BaseURL: "https://search.example", RequestTimeout: MaxRequestTimeout + time.Second},
		{BaseURL: "https://search.example", MaxResults: MaxResults + 1},
		{BaseURL: "https://search.example", MaxResponseBytes: MaxResponseBytes + 1},
		{BaseURL: "https://search.example", SafeSearch: intPtr(3)},
		{BaseURL: "https://search.example", DefaultLanguage: "zh CN"},
		{BaseURL: "https://search.example", Categories: []string{"general,news"}},
	}
	for _, item := range invalid {
		if _, err := New(item); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("invalid config accepted: %+v, err=%v", item, err)
		}
	}
}

func intPtr(v int) *int { return &v }

func TestExecuteNormalizesSearchResultsAndRequest(t *testing.T) {
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"query":"ignored","results":[
			{"title":"  Go Release  ","url":"https://go.dev/doc/","content":"  Latest Go release  ","engine":"brave","score":1.5,"publishedDate":"2026-11-16"},
			{"title":"Bad URL","url":"javascript:alert(1)","content":"must be skipped","engine":"bad"},
			{"title":"Second","url":"https://example.com/path","content":"summary","engine":"duckduckgo","publishedDate":null}
		]}`)
	}))
	defer server.Close()
	cfg := testConfig(server.URL + "/searxng")
	cfg.MaxResults = 2
	cfg.DefaultLanguage = "zh-CN"
	cfg.Categories = []string{"general", "news"}
	cfg.SafeSearch = intPtr(2)
	executor := testExecutor(t, server, cfg)
	result, err := executor.Execute(context.Background(), json.RawMessage(`{"query":" Go release "}`))
	if err != nil { t.Fatal(err) }
	if gotQuery.Get("q") != "Go release" || gotQuery.Get("format") != "json" || gotQuery.Get("safesearch") != "2" {
		t.Fatalf("unexpected request query: %v", gotQuery)
	}
	if gotQuery.Get("language") != "zh-CN" || gotQuery.Get("categories") != "general,news" {
		t.Fatalf("unexpected configured search filters: %v", gotQuery)
	}
	var output searchOutput
	if err := json.Unmarshal(result, &output); err != nil { t.Fatal(err) }
	if output.Query != "Go release" || len(output.Results) != 2 {
		t.Fatalf("unexpected normalized result: %+v", output)
	}
	if output.Results[0].Title != "Go Release" || output.Results[0].PublishedAt != "2026-11-16" {
		t.Fatalf("first result was not normalized: %+v", output.Results[0])
	}
	if output.Results[1].Title != "Second" || output.Results[1].PublishedAt != "" {
		t.Fatalf("null publishedDate should be omitted: %+v", output.Results[1])
	}
}

func TestExecuteReturnsEmptyResultsAsValidOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"results":[]}`)
	}))
	defer server.Close()
	executor := testExecutor(t, server, testConfig(server.URL))
	result, err := executor.Execute(context.Background(), json.RawMessage(`{"query":"no match"}`))
	if err != nil { t.Fatal(err) }
	var output searchOutput
	if err := json.Unmarshal(result, &output); err != nil { t.Fatal(err) }
	if output.Results == nil || len(output.Results) != 0 {
		t.Fatalf("expected an empty JSON array, got %#v", output.Results)
	}
}

func TestExecuteRejectsBadUpstreamResponses(t *testing.T) {
	tests := []struct {
		name string
		status int
		body string
		max int64
		want error
	}{
		{name:"upstream status", status:502, body:`{"error":"secret"}`, want:ErrUpstreamStatus},
		{name:"invalid json", status:200, body:`{"results":`, want:ErrInvalidResponse},
		{name:"missing results", status:200, body:`{"query":"x"}`, want:ErrInvalidResponse},
		{name:"oversized", status:200, body:`{"results":[{"title":"`+strings.Repeat("x",100)+`","url":"https://example.com"}]}`, max:32, want:ErrResponseTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			cfg := testConfig(server.URL)
			if tt.max > 0 { cfg.MaxResponseBytes = tt.max }
			executor := testExecutor(t, server, cfg)
			_, err := executor.Execute(context.Background(), json.RawMessage(`{"query":"x"}`))
			if !errors.Is(err, tt.want) { t.Fatalf("error = %v, want %v", err, tt.want) }
			if tt.want == ErrUpstreamStatus && strings.Contains(err.Error(), "secret") {
				t.Fatalf("upstream body leaked in error: %v", err)
			}
		})
	}
}

func TestExecuteDoesNotFollowRedirects(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		fmt.Fprint(w, `{"results":[]}`)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, nil, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	executor := testExecutor(t, redirect, testConfig(redirect.URL))
	_, err := executor.Execute(context.Background(), json.RawMessage(`{"query":"x"}`))
	if !errors.Is(err, ErrUpstreamStatus) { t.Fatalf("redirect error = %v", err) }
	if targetHits.Load() != 0 { t.Fatalf("redirect target was contacted %d times", targetHits.Load()) }
}

func TestExecuteHonorsTimeoutAndCancellation(t *testing.T) {
	t.Run("client timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer server.Close()
		cfg := testConfig(server.URL)
		cfg.RequestTimeout = 20 * time.Millisecond
		executor := testExecutor(t, server, cfg)
		_, err := executor.Execute(context.Background(), json.RawMessage(`{"query":"x"}`))
		if !errors.Is(err, ErrUpstreamTimeout) { t.Fatalf("timeout error = %v", err) }
	})
	t.Run("parent cancellation", func(t *testing.T) {
		started := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done()
		}))
		defer server.Close()
		executor := testExecutor(t, server, testConfig(server.URL))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := executor.Execute(ctx, json.RawMessage(`{"query":"x"}`))
			done <- err
		}()
		<-started
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) { t.Fatalf("cancel error = %v", err) }
	})
}

func TestExecuteRejectsInvalidArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"results":[]}`)
	}))
	defer server.Close()
	executor := testExecutor(t, server, testConfig(server.URL))
	for _, raw := range []string{`{}`, `{"query":""}`, `{"query":"x","url":"https://evil.example"}`, `{"query":"x"} {}`} {
		if _, err := executor.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("invalid arguments accepted: %s", raw)
		}
	}
}

func TestNormalizeResultURLAndClipUTF8(t *testing.T) {
	for _, raw := range []string{"javascript:alert(1)", "file:///etc/passwd", "https://user:pass@example.com"} {
		if got := normalizeResultURL(raw); got != "" { t.Errorf("unsafe URL %q was retained as %q", raw, got) }
	}
	if got := normalizeResultURL("https://example.com/path#fragment"); strings.Contains(got, "#") {
		t.Fatalf("URL fragment should be removed: %q", got)
	}
	value := strings.Repeat("中", 100)
	clipped := clipUTF8(value, 100)
	if len(clipped) > 100 || !utf8.ValidString(clipped) {
		t.Fatalf("clipped value is invalid UTF-8 or too long: bytes=%d", len(clipped))
	}
}

func TestAllowedSearXNGAddress(t *testing.T) {
	tests := []struct {
		ip string
		allowPrivate bool
		want bool
	}{
		{"8.8.8.8", false, true},
		{"10.0.0.2", false, false},
		{"10.0.0.2", true, true},
		{"127.0.0.1", true, false},
		{"169.254.169.254", true, false},
		{"224.0.0.1", true, false},
		{"192.0.2.1", true, false},
		{"fd00:ec2::254", true, false},
	}
	for _, tt := range tests {
		ip := netip.MustParseAddr(tt.ip)
		if got := allowedSearXNGAddress(ip, tt.allowPrivate); got != tt.want {
			t.Errorf("allowedSearXNGAddress(%s, %v) = %v, want %v", tt.ip, tt.allowPrivate, got, tt.want)
		}
	}
}

func TestNewSafeHTTPClientUsesInjectedResolver(t *testing.T) {
	client := newSafeHTTPClient(normalizedConfig{requestTimeout: time.Second}, fixedResolver{netip.MustParseAddr("8.8.8.8")})
	if client == nil || client.Transport == nil || client.CheckRedirect == nil {
		t.Fatal("safe HTTP client was not fully configured")
	}
}

type fixedResolver struct { addresses []netip.Addr }
func (r fixedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return append([]netip.Addr(nil), r.addresses...), nil
}

