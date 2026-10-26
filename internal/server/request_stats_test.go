package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestStatsRange(t *testing.T) {
	cases := []struct {
		input string
		name  string
		want  time.Duration
		ok    bool
	}{
		{input: "", name: "24h", want: 24 * time.Hour, ok: true},
		{input: "24h", name: "24h", want: 24 * time.Hour, ok: true},
		{input: "7d", name: "7d", want: 7 * 24 * time.Hour, ok: true},
		{input: "30d", name: "30d", want: 30 * 24 * time.Hour, ok: true},
		{input: " 7D ", name: "7d", want: 7 * 24 * time.Hour, ok: true},
		{input: "365d", ok: false},
		{input: "forever", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			name, got, ok := requestStatsRange(tc.input)
			if ok != tc.ok {
				t.Fatalf("requestStatsRange(%q) ok = %v, want %v", tc.input, ok, tc.ok)
			}
			if !tc.ok {
				return
			}
			if name != tc.name || got != tc.want {
				t.Fatalf("requestStatsRange(%q) = (%q, %v), want (%q, %v)", tc.input, name, got, tc.name, tc.want)
			}
		})
	}
}

func TestWriteRequestStatsDatabaseErrorHidesInternalDetails(t *testing.T) {
	internalErr := errors.New("unknown column provider_id in query")
	recorder := httptest.NewRecorder()

	writeRequestStatsDatabaseError(recorder, internalErr)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	body := recorder.Body.String()
	if strings.Contains(body, internalErr.Error()) || strings.Contains(body, "unknown column") {
		t.Fatalf("database diagnostic leaked to API response: %s", body)
	}
	if !strings.Contains(body, "查询请求统计失败") || !strings.Contains(body, "database_error") {
		t.Fatalf("unexpected sanitized error response: %s", body)
	}
}
