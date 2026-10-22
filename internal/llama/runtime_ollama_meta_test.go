package llama

import "testing"

func TestOllamaCatalogMeta(t *testing.T) {
	q, p := ollamaCatalogMeta("Q4_K_M", "4.0B", "llama3.2:latest")
	if q != "Q4_K_M" || p != "4.0B" {
		t.Fatalf("details: got quant=%q param=%q", q, p)
	}
	q, p = ollamaCatalogMeta("", "", "medgemma:4b-q4_K_M")
	if q != "Q4_K_M" || p != "" {
		t.Fatalf("name fallback: got quant=%q param=%q", q, p)
	}
}
