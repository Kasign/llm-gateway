package llama

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const DefaultSlotsResponseLimit int64 = 2 << 20

// SlotSnapshot 仅包含安全白名单字段；不会保留上游原始 JSON 或请求参数。
type SlotSnapshot struct {
	SampledAt time.Time  `json:"sampled_at"`
	Slots     []SlotInfo `json:"slots"`
}

type SlotInfo struct {
	ID            *int64 `json:"id,omitempty"`
	IsProcessing  *bool  `json:"is_processing,omitempty"`
	ContextSize   *int64 `json:"n_ctx,omitempty"`
	DecodedTokens *int64 `json:"n_decoded,omitempty"`
}

// ParseSlots 仅解码明确允许的字段。未知字段（包括 params、generation_prompt、samplers）被忽略。
func ParseSlots(body []byte, sampledAt time.Time) (*SlotSnapshot, error) {
	var raw []struct {
		ID           *int64 `json:"id"`
		IsProcessing *bool  `json:"is_processing"`
		ContextSize  *int64 `json:"n_ctx"`
		NextToken    *struct {
			Decoded *int64 `json:"n_decoded"`
		} `json:"next_token"`
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, errors.New("slots response is empty")
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode slots response: %w", err)
	}
	out := &SlotSnapshot{SampledAt: sampledAt, Slots: make([]SlotInfo, 0, len(raw))}
	for _, item := range raw {
		slot := SlotInfo{ID: item.ID, IsProcessing: item.IsProcessing, ContextSize: item.ContextSize}
		if item.NextToken != nil {
			slot.DecodedTokens = item.NextToken.Decoded
		}
		out.Slots = append(out.Slots, slot)
	}
	return out, nil
}
