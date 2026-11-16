package server

import "testing"

func TestOllamaPullJobListed(t *testing.T) {
	now := int64(1_000_000)
	if !ollamaPullJobListed("downloading", 0, now) {
		t.Fatal("downloading should list")
	}
	if ollamaPullJobListed("", now, now) {
		t.Fatal("empty status hidden")
	}
	if !ollamaPullJobListed("ready", now-100, now) {
		t.Fatal("recent ready should list")
	}
	if ollamaPullJobListed("ready", now-ollamaPullDoneVisibleSec-1, now) {
		t.Fatal("old ready hidden")
	}
	if !ollamaPullJobListed("download_failed", now-3600, now) {
		t.Fatal("recent failed should list")
	}
}

func TestOllamaPullJobInterrupted(t *testing.T) {
	cases := []struct {
		status  string
		running bool
		want    bool
	}{
		{status: "downloading", running: false, want: true},
		{status: "downloading", running: true, want: false},
		{status: "ready", running: false, want: false},
		{status: "download_failed", running: false, want: false},
		{status: "download_cancelled", running: false, want: false},
	}
	for _, tc := range cases {
		if got := ollamaPullJobInterrupted(tc.status, tc.running); got != tc.want {
			t.Errorf("ollamaPullJobInterrupted(%q, %v) = %v, want %v", tc.status, tc.running, got, tc.want)
		}
	}
}
