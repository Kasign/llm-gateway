package store

import (
	"testing"
	"time"
)

func TestValidateModelPrice(t *testing.T) {
	valid := ModelPrice{
		ProviderID: "  DeepSeek ", ModelID: "deepseek-chat",
		InputPricePerMillion: "0.27", OutputPricePerMillion: "1.10",
		Currency: "usd", EffectiveAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := validateModelPrice(valid); err != nil {
		t.Fatalf("valid price rejected: %v", err)
	}

	tests := []struct {
		name string
		edit func(*ModelPrice)
	}{
		{"missing provider", func(p *ModelPrice) { p.ProviderID = " " }},
		{"missing model", func(p *ModelPrice) { p.ModelID = "" }},
		{"negative input price", func(p *ModelPrice) { p.InputPricePerMillion = "-0.1" }},
		{"excess fractional precision", func(p *ModelPrice) { p.OutputPricePerMillion = "0.1234567890123" }},
		{"unsupported currency", func(p *ModelPrice) { p.Currency = "CNY" }},
		{"missing effective time", func(p *ModelPrice) { p.EffectiveAt = time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := valid
			tt.edit(&p)
			if err := validateModelPrice(p); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateModelPriceAllowsZero(t *testing.T) {
	p := ModelPrice{
		ProviderID: "local-remote", ModelID: "free-model",
		InputPricePerMillion: "0", OutputPricePerMillion: "0.000000000001",
		Currency: "USD", EffectiveAt: time.Now(),
	}
	if err := validateModelPrice(p); err != nil {
		t.Fatalf("zero price should be valid: %v", err)
	}
}
