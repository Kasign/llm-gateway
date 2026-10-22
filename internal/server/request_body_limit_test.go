package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadLimitedRequestBody(t *testing.T) {
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
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			got, err := readLimitedRequestBody(rec, req, tc.limit)
			if tc.wantError {
				var maxErr *http.MaxBytesError
				if !errors.As(err, &maxErr) {
					t.Fatalf("error = %v, want *http.MaxBytesError", err)
				}
				if int64(len(got)) > tc.limit {
					t.Fatalf("read %d bytes beyond limit %d", len(got), tc.limit)
				}
				return
			}
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if gotString := string(got); gotString != tc.body {
				t.Fatalf("body = %q, want %q", gotString, tc.body)
			}
		})
	}
}
