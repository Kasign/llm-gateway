package server

import (
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
