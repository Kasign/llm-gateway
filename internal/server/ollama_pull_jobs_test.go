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
