package llama

import (
	"errors"
	"strings"
	"testing"
)

func TestReadProxyResponseBodyLimit(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		limit     int64
		wantError bool
	}{
		{name: "exact limit", body: "0123456789", limit: 10},
		{name: "over limit", body: "01234567890", limit: 10, wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readProxyResponseBody(strings.NewReader(tc.body), tc.limit)
			if tc.wantError {
				if !errors.Is(err, errUpstreamResponseTooLarge) {
					t.Fatalf("error = %v, want response-too-large error", err)
				}
				if len(got) != 0 {
					t.Fatalf("oversized body should not be returned for forwarding, got %d bytes", len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if string(got) != tc.body {
				t.Fatalf("body = %q, want %q", got, tc.body)
			}
		})
	}
}
