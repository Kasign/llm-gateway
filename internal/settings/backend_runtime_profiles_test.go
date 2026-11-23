package settings

import "testing"

func intPtr(v int) *int { return &v }
func boolPtr(v bool) *bool { return &v }

func validRuntimeProfile() BackendRuntimeProfile {
	return BackendRuntimeProfile{
		ID: "profile-test-1", Name: "NUC CPU baseline", Backend: "llamacpp",
		ModelID: "Qwen3.5-9B-Q4_K_M",
		Parameters: BackendRuntimeParameters{
			Threads: intPtr(8), ThreadsBatch: intPtr(8),
			BatchSize: intPtr(512), UBatchSize: intPtr(128),
			ContextSize: intPtr(8192), GPULayers: intPtr(0),
			FlashAttention: boolPtr(false), CacheTypeK: "q8_0", CacheTypeV: "q8_0",
		},
	}
}

func TestBackendRuntimeProfileNormalizeAndValidate(t *testing.T) {
	got, err := validRuntimeProfile().NormalizeAndValidate()
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "user_declared" || got.Version != 1 || got.UpdatedAt.IsZero() {
		t.Fatalf("profile metadata not normalized: %+v", got)
	}
	if got.Parameters.Threads == nil || *got.Parameters.Threads != 8 ||
		got.Parameters.FlashAttention == nil || *got.Parameters.FlashAttention {
		t.Fatalf("runtime parameter values changed: %+v", got.Parameters)
	}
}

func TestBackendRuntimeProfileRejectsInvalidRangesAndUnknownBackend(t *testing.T) {
	profile := validRuntimeProfile()
	profile.Parameters.UBatchSize = intPtr(1024)
	if _, err := profile.NormalizeAndValidate(); err == nil {
		t.Fatal("ubatch_size greater than batch_size must be rejected")
	}
	profile = validRuntimeProfile()
	profile.Backend = "ollama"
	if _, err := profile.NormalizeAndValidate(); err == nil {
		t.Fatal("unsupported backend must be rejected")
	}
	profile = validRuntimeProfile()
	profile.Parameters.CacheTypeK = "q8 0"
	if _, err := profile.NormalizeAndValidate(); err == nil {
		t.Fatal("cache type containing spaces must be rejected")
	}
}

func TestBackendRuntimeProfileRequiresAtLeastOneDeclaredParameter(t *testing.T) {
	profile := validRuntimeProfile()
	profile.Parameters = BackendRuntimeParameters{}
	if _, err := profile.NormalizeAndValidate(); err == nil {
		t.Fatal("empty parameter profile must be rejected")
	}
}
