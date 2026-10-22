package server

import "testing"

func TestDisplayClientName(t *testing.T) {
	cases := []struct {
		name, ua, want string
	}{
		{name: "Codex App", ua: "curl/8.0", want: "Codex App"},
		{name: "  ", ua: "codex_cli_rs/0.98.0 (Mac OS; arm64)", want: "Codex"},
		{name: "", ua: "Codex/1.2.3", want: "Codex"},
		{name: "", ua: "curl/8.7.1", want: "curl"},
		{name: "", ua: "Mozilla/5.0 (Macintosh) AppleWebKit/537.36", want: ""},
		{name: "", ua: "", want: ""},
	}
	for _, tc := range cases {
		if got := displayClientName(tc.name, tc.ua); got != tc.want {
			t.Errorf("displayClientName(%q, %q)=%q want %q", tc.name, tc.ua, got, tc.want)
		}
	}
}
