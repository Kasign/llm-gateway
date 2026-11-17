package server

import (
	"encoding/json"
	"math"

	"github.com/whl/llm-gateway/internal/logutil"
	"github.com/whl/llm-gateway/internal/tools/coordinator"
)

type modelRoundUsage struct {
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	InputTokens      *int64 `json:"input_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
	TotalTokens      *int64 `json:"total_tokens"`
}

// aggregateCoordinatorUsage 只在每次模型调用都返回完整 usage 时给出请求级总量。
// 不完整时返回空结果，避免把最后一轮 usage 误报成整个请求的总用量。
func aggregateCoordinatorUsage(result coordinator.Result) (logutil.TokenHarvest, bool) {
	if result.ModelRounds <= 0 || len(result.RoundUsage) != result.ModelRounds {
		return logutil.TokenHarvest{}, false
	}
	var inputTotal, outputTotal, totalTotal int64
	for _, raw := range result.RoundUsage {
		var usage modelRoundUsage
		if len(raw) == 0 || json.Unmarshal(raw, &usage) != nil {
			return logutil.TokenHarvest{}, false
		}
		input := usage.PromptTokens
		if input == nil {
			input = usage.InputTokens
		}
		output := usage.CompletionTokens
		if output == nil {
			output = usage.OutputTokens
		}
		if input == nil || output == nil || *input < 0 || *output < 0 {
			return logutil.TokenHarvest{}, false
		}
		total := usage.TotalTokens
		if total == nil {
			if *input > math.MaxInt64-*output {
				return logutil.TokenHarvest{}, false
			}
			value := *input + *output
			total = &value
		}
		if *total < 0 || inputTotal > math.MaxInt64-*input ||
			outputTotal > math.MaxInt64-*output || totalTotal > math.MaxInt64-*total {
			return logutil.TokenHarvest{}, false
		}
		inputTotal += *input
		outputTotal += *output
		totalTotal += *total
	}
	input, output, total := inputTotal, outputTotal, totalTotal
	return logutil.TokenHarvest{
		InputTokens: &input,
		OutputTokens: &output,
		TotalTokens: &total,
		Source: "usage",
	}, true
}

func (s *Server) storeCoordinatorUsage(requestID string, result coordinator.Result) {
	if s == nil || result.ModelRounds <= 0 {
		return
	}
	usage, complete := aggregateCoordinatorUsage(result)
	cs := s.callGet(requestID)
	if cs == nil {
		return
	}
	cs.mu.Lock()
	cs.coordinatorUsageSet = true
	if complete {
		cs.tokens = usage
	} else {
		cs.tokens = logutil.TokenHarvest{}
	}
	cs.mu.Unlock()
}
