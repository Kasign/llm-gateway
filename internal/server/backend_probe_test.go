package server

import "testing"

func TestUpstreamProbeBlockOK(t *testing.T) {
	if upstreamProbeBlockOK(map[string]interface{}{
		"base_url": "http://127.0.0.1:11434",
		"checks": []map[string]interface{}{
			{"path": "/api/tags", "http_status": 0, "error": "connection refused"},
		},
	}) {
		t.Fatal("expected false when check has error")
	}
	if upstreamProbeBlockOK(map[string]interface{}{
		"base_url": "http://127.0.0.1:11434",
		"checks": []map[string]interface{}{
			{"path": "/api/tags", "http_status": 401},
		},
	}) {
		t.Fatal("expected false for HTTP 401")
	}
	if !upstreamProbeBlockOK(map[string]interface{}{
		"base_url": "http://127.0.0.1:11434",
		"checks": []map[string]interface{}{
			{"path": "/api/tags", "http_status": 200},
		},
	}) {
		t.Fatal("expected true for HTTP 200 without error")
	}
	if upstreamProbeBlockOK(map[string]interface{}{
		"base_url":  "http://127.0.0.1:11434",
		"reachable": false,
		"checks": []map[string]interface{}{
			{"path": "/api/tags", "http_status": 200},
		},
	}) {
		t.Fatal("reachable=false must win over HTTP 200 checks")
	}
}
