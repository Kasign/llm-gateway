package server

import "testing"

func TestUpstreamPathWithQuery(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		query string
		want  string
	}{
		{name: "without query", path: "/v1/chat/completions", want: "/v1/chat/completions"},
		{name: "preserve encoded query", path: "/v1/responses", query: "include=reasoning&metadata%5Bx%5D=a%2Fb", want: "/v1/responses?include=reasoning&metadata%5Bx%5D=a%2Fb"},
		{name: "append to existing query", path: "/v1/responses?existing=1", query: "other=2", want: "/v1/responses?existing=1&other=2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := upstreamPathWithQuery(tc.path, tc.query); got != tc.want {
				t.Fatalf("upstreamPathWithQuery() = %q, want %q", got, tc.want)
			}
		})
	}
}
