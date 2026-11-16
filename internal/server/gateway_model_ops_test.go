package server

import (
	"errors"
	"testing"
)

func TestDeleteModelAfterUnloadStopsOnUnloadFailure(t *testing.T) {
	wantErr := errors.New("upstream unload failed")
	fileRemoved := false
	recordDeleted := false

	err := deleteModelAfterUnload(
		func() error { return wantErr },
		func() error {
			fileRemoved = true
			return nil
		},
		func() error {
			recordDeleted = true
			return nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("deleteModelAfterUnload error = %v, want %v", err, wantErr)
	}
	if fileRemoved {
		t.Fatal("model file was removed after upstream unload failed")
	}
	if recordDeleted {
		t.Fatal("model record was deleted after upstream unload failed")
	}
}

func TestDeleteModelAfterUnloadContinuesAfterSuccessfulUnload(t *testing.T) {
	order := make([]string, 0, 3)
	err := deleteModelAfterUnload(
		func() error {
			order = append(order, "unload")
			return nil
		},
		func() error {
			order = append(order, "remove")
			return nil
		},
		func() error {
			order = append(order, "delete")
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"unload", "remove", "delete"}
	if len(order) != len(want) {
		t.Fatalf("operation order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("operation order = %v, want %v", order, want)
		}
	}
}
