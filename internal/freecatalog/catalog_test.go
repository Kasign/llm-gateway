package freecatalog

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validCatalogFixture() Catalog {
	checkedAt := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	quotaValue := json.Number("10000")
	return Catalog{
		Version: 1,
		Providers: []Provider{{
			ID: "cloudflare", Name: "Cloudflare Workers AI", Website: "https://www.cloudflare.com",
			DocsURL: "https://developers.cloudflare.com/workers-ai/",
		}},
		Models: []Model{{
			ProviderID: "cloudflare", ID: "@cf/example/model", Name: "Example Model",
			Protocols: []string{"chat_completions"}, Capabilities: []string{"streaming"},
			EvidenceIDs: []string{"workers-ai-docs"},
		}},
		Plans: []Plan{{
			ID: "cloudflare-workers-free", ProviderID: "cloudflare", Name: "Workers Free",
			Type: PlanTypeDailyAllocation, Status: PlanStatusActive,
			Eligibility: Eligibility{
				Regions: RegionRule{Mode: RegionModeUnspecified},
				PaymentMethod: RequirementUnknown,
			},
			DataPolicy: DataPolicy{Training: DataUseUnknown, Retention: DataUseUnknown},
			Quotas: []Quota{{
				Metric: "neurons", Value: &quotaValue, Unit: "neurons", Period: "day",
				Scope: "account", State: QuotaStateKnown, ResetTimezone: "UTC",
				EvidenceIDs: []string{"workers-ai-docs"},
			}},
			Models: []PlanModel{{
				ModelID: "@cf/example/model", Availability: ModelAvailabilityUnknown,
				EvidenceIDs: []string{"workers-ai-docs"},
			}},
			VerifiedAt: checkedAt, NextReviewAt: checkedAt.AddDate(0, 0, 30),
			EvidenceIDs: []string{"workers-ai-docs"},
		}},
		Evidence: []Evidence{{
			ID: "workers-ai-docs", URL: "https://developers.cloudflare.com/workers-ai/platform/pricing/",
			Kind: EvidencePricingPage, CheckedAt: checkedAt,
			Supports: []string{"plans.cloudflare-workers-free.quotas.neurons"},
		}},
	}
}

func TestLoadEmbeddedCatalog(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Version != 1 || len(catalog.Providers) != 0 || len(catalog.Models) != 0 || len(catalog.Plans) != 0 {
		t.Fatalf("unexpected embedded catalog: %+v", catalog)
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	_, err := Decode([]byte(`{"version":1,"providers":[],"models":[],"plans":[],"evidence":[],"unknown":true}`))
	if err == nil {
		t.Fatal("unknown JSON field was accepted")
	}
}

func TestCatalogValidatePreservesQuotaUnitsAndUnknownValues(t *testing.T) {
	catalog := validCatalogFixture()
	if err := catalog.Validate(); err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
	quota := catalog.Plans[0].Quotas[0]
	if quota.Value == nil || *quota.Value != 10000 || quota.Unit != "neurons" || quota.Period != "day" {
		t.Fatalf("quota unit or value changed: %+v", quota)
	}

	unknown := Quota{
		Metric: "rpm", Unit: "requests", Period: "minute", Scope: "model",
		State: QuotaStateUnknown, EvidenceIDs: []string{"workers-ai-docs"},
	}
	catalog.Plans[0].Quotas = append(catalog.Plans[0].Quotas, unknown)
	if err := catalog.Validate(); err != nil {
		t.Fatalf("unknown quota without numeric value rejected: %v", err)
	}

	zero := json.Number("0")
	catalog.Plans[0].Quotas[1].Value = &zero
	if err := catalog.Validate(); err == nil {
		t.Fatal("unknown quota with numeric zero was accepted")
	}
}

func TestCatalogValidateAllowsExplicitZeroAndRejectsMissingKnownValue(t *testing.T) {
	catalog := validCatalogFixture()
	zero := float64(0)
	catalog.Plans[0].Quotas[0].Value = &zero
	if err := catalog.Validate(); err != nil {
		t.Fatalf("explicit zero quota rejected: %v", err)
	}
	catalog.Plans[0].Quotas[0].Value = nil
	if err := catalog.Validate(); err == nil {
		t.Fatal("known quota without value was accepted")
	}
}

func TestCatalogValidateRequiresModelAndEvidenceReferences(t *testing.T) {
	catalog := validCatalogFixture()
	catalog.Plans[0].Models[0].ModelID = "missing/model"
	if err := catalog.Validate(); err == nil {
		t.Fatal("unknown model reference was accepted")
	}

	catalog = validCatalogFixture()
	catalog.Plans[0].EvidenceIDs = []string{"missing-evidence"}
	if err := catalog.Validate(); err == nil {
		t.Fatal("unknown evidence reference was accepted")
	}
}

func TestPlanEffectiveStatus(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	plan := Plan{Status: PlanStatusActive, NextReviewAt: now.Add(-time.Minute)}
	if got := plan.EffectiveStatus(now); got != PlanStatusReviewRequired {
		t.Fatalf("effective status = %q, want review_required", got)
	}

	expiredAt := now.Add(-time.Second)
	plan = Plan{Status: PlanStatusActive, EffectiveUntil: &expiredAt}
	if got := plan.EffectiveStatus(now); got != PlanStatusExpired {
		t.Fatalf("effective status = %q, want expired", got)
	}

	plan = Plan{Status: PlanStatusWithdrawn, EffectiveUntil: &expiredAt}
	if got := plan.EffectiveStatus(now); got != PlanStatusWithdrawn {
		t.Fatalf("withdrawn status changed to %q", got)
	}
}

func TestDecodeRejectsTrailingJSON(t *testing.T) {
	raw, err := json.Marshal(Catalog{Version: 1, Providers: []Provider{}, Models: []Model{}, Plans: []Plan{}, Evidence: []Evidence{}})
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte(" {}")...)
	_, err = Decode(raw)
	if err == nil || !strings.Contains(err.Error(), "多余 JSON") {
		t.Fatalf("trailing JSON error = %v", err)
	}
}

func TestQuotaDecimalPrecisionIsPreserved(t *testing.T) {
	catalog := validCatalogFixture()
	value := json.Number("0.000000000123456789")
	catalog.Plans[0].Quotas[0].Value = &value

	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	got := decoded.Plans[0].Quotas[0].Value
	if got == nil || got.String() != value.String() {
		t.Fatalf("quota decimal changed: got %v, want %s", got, value)
	}
}

func TestQuotaRejectsInvalidAndNegativeDecimalValues(t *testing.T) {
	for _, rawValue := range []string{"-0.01", "not-a-number", "NaN", "Inf"} {
		catalog := validCatalogFixture()
		value := json.Number(rawValue)
		catalog.Plans[0].Quotas[0].Value = &value
		if err := catalog.Validate(); err == nil {
			t.Errorf("quota value %q should be rejected", rawValue)
		}
	}
}
