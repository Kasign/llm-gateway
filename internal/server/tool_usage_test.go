package server

import (
	"encoding/json"
	"testing"

	"github.com/whl/llm-gateway/internal/tools/coordinator"
)

func TestAggregateCoordinatorUsageSumsAllModelRounds(t *testing.T) {
	result := coordinator.Result{
		ModelRounds: 2,
		RoundUsage: []json.RawMessage{
			json.RawMessage(`{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}`),
			json.RawMessage(`{"prompt_tokens":20,"completion_tokens":6,"total_tokens":26}`),
		},
	}
	usage, ok := aggregateCoordinatorUsage(result)
	if !ok {
		t.Fatal("complete usage was rejected")
	}
	if *usage.InputTokens != 30 || *usage.OutputTokens != 10 || *usage.TotalTokens != 40 || usage.Source != "usage" {
		t.Fatalf("unexpected aggregate usage: %+v", usage)
	}
}

func TestAggregateCoordinatorUsageSupportsResponsesFieldNames(t *testing.T) {
	result := coordinator.Result{
		ModelRounds: 1,
		RoundUsage: []json.RawMessage{json.RawMessage(`{"input_tokens":7,"output_tokens":3}`)},
	}
	usage, ok := aggregateCoordinatorUsage(result)
	if !ok || *usage.InputTokens != 7 || *usage.OutputTokens != 3 || *usage.TotalTokens != 10 {
		t.Fatalf("unexpected aggregate usage: %+v, complete=%v", usage, ok)
	}
}

func TestAggregateCoordinatorUsageRejectsIncompleteRounds(t *testing.T) {
	result := coordinator.Result{
		ModelRounds: 2,
		RoundUsage: []json.RawMessage{json.RawMessage(`{"prompt_tokens":10,"completion_tokens":4}`)},
	}
	if _, ok := aggregateCoordinatorUsage(result); ok {
		t.Fatal("partial usage was reported as complete")
	}
}

func TestAggregateCoordinatorUsageRejectsOverflow(t *testing.T) {
	result := coordinator.Result{
		ModelRounds: 2,
		RoundUsage: []json.RawMessage{
			json.RawMessage(`{"prompt_tokens":9223372036854775807,"completion_tokens":1}`),
			json.RawMessage(`{"prompt_tokens":1,"completion_tokens":1}`),
		},
	}
	if _, ok := aggregateCoordinatorUsage(result); ok {
		t.Fatal("overflowing usage was reported as complete")
	}
}
