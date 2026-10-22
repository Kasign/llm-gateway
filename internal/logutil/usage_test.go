package logutil

import "testing"

func TestParseUsageChatPromptCompletion(t *testing.T) {
	raw := `data: {"usage":{"prompt_tokens":11,"completion_tokens":22,"total_tokens":33}}`
	in, out, total, ok := ParseUsageChunk([]byte(raw))
	if !ok || in != 11 || out != 22 || total != 33 {
		t.Fatalf("chat usage: in=%d out=%d total=%d ok=%v", in, out, total, ok)
	}
}

func TestParseUsageResponsesInputOutput(t *testing.T) {
	raw := `data: {"usage":{"input_tokens":8,"output_tokens":16,"total_tokens":24}}`
	in, out, total, ok := ParseUsageChunk([]byte(raw))
	if !ok || in != 8 || out != 16 || total != 24 {
		t.Fatalf("responses usage: in=%d out=%d total=%d ok=%v", in, out, total, ok)
	}
}

func TestParseUsageResponseCompletedNested(t *testing.T) {
	raw := `data: {"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":7}}}`
	in, out, total, ok := ParseUsageChunk([]byte(raw))
	if !ok || in != 3 || out != 7 || total != 10 {
		t.Fatalf("nested usage: in=%d out=%d total=%d ok=%v", in, out, total, ok)
	}
}

func TestParseUsageAllZeroNotOK(t *testing.T) {
	raw := `data: {"usage":{"input_tokens":0,"output_tokens":0}}`
	if _, _, _, ok := ParseUsageChunk([]byte(raw)); ok {
		t.Fatal("all-zero usage should not count as parsed")
	}
}

func TestParseUsageBodyResponses(t *testing.T) {
	in, out, total := ParseUsageBody([]byte(`{"usage":{"input_tokens":4,"output_tokens":5}}`))
	if in == nil || out == nil || total == nil || *in != 4 || *out != 5 || *total != 9 {
		t.Fatalf("body usage: %v %v %v", in, out, total)
	}
}

func TestApplyStreamChunkTokensSplitAcrossChunks(t *testing.T) {
	var h TokenHarvest
	var carry string
	ApplyStreamChunkTokens(&h, []byte(`data: {"usage":{"input_tokens":6,"out`), &carry)
	if !h.Empty() {
		t.Fatalf("half line should wait, got %+v", h)
	}
	ApplyStreamChunkTokens(&h, []byte(`put_tokens":9}}`+"\n"), &carry)
	if h.Empty() || h.InputTokens == nil || *h.InputTokens != 6 || *h.OutputTokens != 9 {
		t.Fatalf("split usage not joined: %+v", h)
	}
}

func TestFlushStreamTokenCarryWithoutNewline(t *testing.T) {
	var h TokenHarvest
	var carry string
	ApplyStreamChunkTokens(&h, []byte(`data: {"usage":{"prompt_tokens":1,"completion_tokens":2}}`), &carry)
	if !h.Empty() {
		t.Fatalf("no newline yet, got %+v", h)
	}
	FlushStreamTokenCarry(&h, &carry)
	if h.Empty() || *h.InputTokens != 1 || *h.OutputTokens != 2 {
		t.Fatalf("flush leftover: %+v", h)
	}
}

func TestTokensFromResponseCompletedSSE(t *testing.T) {
	body := []byte("event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":9}}}\n\n")
	h := TokensFromResponse(body)
	if h.Empty() || h.InputTokens == nil || *h.InputTokens != 2 || *h.OutputTokens != 9 {
		t.Fatalf("sse harvest=%+v", h)
	}
}
