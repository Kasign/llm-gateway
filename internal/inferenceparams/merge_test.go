package inferenceparams

import "testing"

func TestMergeChatBody_clientFirst(t *testing.T) {
	temp := 0.2
	defs := Params{Temperature: &temp}
	raw := []byte(`{"messages":[],"temperature":0.9}`)
	out := MergeChatBody("llamacpp", ModeClientFirst, defs, raw)
	if string(out) != string(raw) {
		t.Fatalf("expected client temp preserved, got %s", out)
	}
	raw2 := []byte(`{"messages":[]}`)
	out2 := MergeChatBody("llamacpp", ModeClientFirst, defs, raw2)
	if !containsStr(string(out2), `"temperature":0.2`) {
		t.Fatalf("expected injected temp, got %s", out2)
	}
}

func TestMergeChatBody_gatewayOnly(t *testing.T) {
	temp := 0.2
	defs := Params{Temperature: &temp}
	raw := []byte(`{"messages":[],"temperature":0.9}`)
	out := MergeChatBody("llamacpp", ModeGatewayOnly, defs, raw)
	if !containsStr(string(out), `"temperature":0.2`) {
		t.Fatalf("expected gateway temp, got %s", out)
	}
}

func TestMergeEffective_savedOverridesFactory(t *testing.T) {
	factory := DefaultsLlamaCpp(nil)
	saved := Params{Temperature: fp(0.15)}
	out := MergeEffective(factory, saved)
	if out.Temperature == nil || *out.Temperature != 0.15 {
		t.Fatalf("temperature want 0.15 got %v", out.Temperature)
	}
	if out.TopP == nil || *out.TopP != *factory.TopP {
		t.Fatalf("top_p should stay factory %v got %v", factory.TopP, out.TopP)
	}
}

func TestMergeEffective_unsavedUsesFactory(t *testing.T) {
	factory := DefaultsOllama(nil)
	out := MergeEffective(factory, Params{})
	if out.Temperature == nil || *out.Temperature != 0.8 {
		t.Fatalf("expected ollama factory temp 0.8")
	}
	if out.KeepAlive == nil || *out.KeepAlive != "24h" {
		t.Fatalf("expected keep_alive 24h")
	}
}

func TestMergeEffective_inheritClearsThinking(t *testing.T) {
	factory := DefaultsLlamaCpp(nil)
	inherit := ""
	out := MergeEffective(factory, Params{EnableThinking: &inherit})
	if out.EnableThinking != nil {
		t.Fatalf("inherit should clear enable_thinking injection")
	}
}

func TestThinkingClientFirstKeepsClientOn(t *testing.T) {
	off := "false"
	raw := []byte(`{"reasoning":{"effort":"low"},"enable_thinking":true}`)
	out := MergeChatBody("ollama", ModeClientFirst, Params{EnableThinking: &off}, raw)
	s := string(out)
	if !containsStr(s, `"reasoning"`) || !containsStr(s, `"enable_thinking":true`) {
		t.Fatalf("client on should win under client_first, got %s", s)
	}
}

func TestThinkingMixedOnWinsAndDropsOffMarker(t *testing.T) {
	off := "false"
	raw := []byte(`{"reasoning":{"effort":"low"},"enable_thinking":false}`)
	out := MergeChatBody("llamacpp", ModeClientFirst, Params{EnableThinking: &off}, raw)
	s := string(out)
	if !containsStr(s, `"reasoning"`) || containsStr(s, `"enable_thinking"`) {
		t.Fatalf("on signal should win and off marker should drop, got %s", s)
	}
}

func TestThinkingGatewayOnlyOffStripsAllOnFields(t *testing.T) {
	off := "false"
	raw := []byte(`{"reasoning":{"effort":"low"},"reasoning_effort":"low","thinking":{"type":"enabled"},"think":true,"include":["reasoning.encrypted_content","file_search"],"chat_template_kwargs":{"enable_thinking":true}}`)
	out := MergeChatBody("ollama", ModeGatewayOnly, Params{EnableThinking: &off}, raw)
	s := string(out)
	for _, gone := range []string{`"reasoning"`, `"thinking"`, `"think"`, "reasoning.encrypted_content", "enable_thinking"} {
		if containsStr(s, gone) {
			t.Fatalf("field %s should be removed, got %s", gone, s)
		}
	}
	if !containsStr(s, `"file_search"`) {
		t.Fatalf("non-reasoning include should stay, got %s", s)
	}
}

func TestThinkingClientSilentUsesConfigOff(t *testing.T) {
	off := "false"
	raw := []byte(`{"messages":[]}`)
	llama := string(MergeChatBody("llamacpp", ModeClientFirst, Params{EnableThinking: &off}, raw))
	if !containsStr(llama, `"enable_thinking":false`) {
		t.Fatalf("llamacpp should write false, got %s", llama)
	}
	ollama := string(MergeChatBody("ollama", ModeClientFirst, Params{EnableThinking: &off}, raw))
	if containsStr(ollama, "enable_thinking") || containsStr(ollama, `"think"`) {
		t.Fatalf("ollama off should not add thinking fields, got %s", ollama)
	}
}

func TestThinkingConfigOnFillsEmptyBody(t *testing.T) {
	on := "true"
	raw := []byte(`{"messages":[]}`)
	llama := string(MergeChatBody("llamacpp", ModeGatewayOnly, Params{EnableThinking: &on}, raw))
	if !containsStr(llama, `"enable_thinking":true`) {
		t.Fatalf("llamacpp on should write true, got %s", llama)
	}
	ollama := string(MergeChatBody("ollama", ModeGatewayOnly, Params{EnableThinking: &on}, raw))
	if !containsStr(ollama, `"think":true`) {
		t.Fatalf("ollama on should write think, got %s", ollama)
	}
}

func TestApplyThinkingInheritLeavesReasoning(t *testing.T) {
	raw := []byte(`{"reasoning":{"effort":"low"}}`)
	out := MergeChatBody("ollama", ModeClientFirst, Params{}, raw)
	if !containsStr(string(out), `"reasoning"`) {
		t.Fatalf("inherit should keep reasoning, got %s", out)
	}
}

func TestCapMaxOutputTokens(t *testing.T) {
	capN := 8192
	raw := []byte(`{"max_output_tokens":20000,"max_tokens":9000}`)
	out := MergeChatBody("llamacpp", ModeClientFirst, Params{MaxOutputCap: &capN}, raw)
	s := string(out)
	if !containsStr(s, `"max_output_tokens":8192`) || !containsStr(s, `"max_tokens":8192`) {
		t.Fatalf("cap not applied: %s", s)
	}
}

func containsStr(s, sub string) bool {
	return len(sub) <= len(s) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
