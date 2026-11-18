package llama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseSlotsKeepsOnlyAllowlistedFields(t *testing.T) {
	body := []byte(`[{"id":2,"is_processing":true,"n_ctx":8192,"next_token":{"n_decoded":31},"id_task":999,"params":{"temperature":0.7},"generation_prompt":"must not persist","samplers":["top_k"]}]`)
	got, err := ParseSlots(body, time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Slots) != 1 {
		t.Fatalf("slots=%d", len(got.Slots))
	}
	slot := got.Slots[0]
	if slot.ID == nil || *slot.ID != 2 || slot.IsProcessing == nil || !*slot.IsProcessing || slot.ContextSize == nil || *slot.ContextSize != 8192 || slot.DecodedTokens == nil || *slot.DecodedTokens != 31 {
		t.Fatalf("unexpected slot: %#v", slot)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"must not persist", "temperature", "top_k", "id_task", "params", "generation_prompt", "samplers"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("forbidden upstream field persisted: %q", forbidden)
		}
	}
}

func TestParseSlotsRejectsMalformedPayload(t *testing.T) {
	if _, err := ParseSlots([]byte(`{"not":"an array"}`), time.Now()); err == nil {
		t.Fatal("expected array decoding error")
	}
}


func TestFetchSlotsUsesConfiguredAuth(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"id":1,"is_processing":false}]`))
	}))
	defer srv.Close()

	client := New(srv.URL, 0, "llamacpp", "upstream-secret")
	status, body, err := client.FetchSlots(context.Background(), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || gotPath != "/slots" || gotAuth != "Bearer upstream-secret" {
		t.Fatalf("status=%d path=%q auth=%q", status, gotPath, gotAuth)
	}
	if _, err := ParseSlots(body, time.Now()); err != nil {
		t.Fatalf("response should be parseable: %v", err)
	}
}

func TestFetchSlotsEnforcesResponseLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("12345"))
	}))
	defer srv.Close()

	client := New(srv.URL, 0, "llamacpp", "")
	_, _, err := client.FetchSlots(context.Background(), 4)
	if err != ErrObservationResponseTooLarge {
		t.Fatalf("error=%v", err)
	}
}
