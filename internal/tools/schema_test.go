package tools

import (
	"errors"
	"strings"
	"testing"
)

const testSchema = `{"type":"object","properties":{"query":{"type":"string","minLength":1},"limit":{"type":"integer","enum":[1,2]},"enabled":{"type":"boolean"},"tags":{"type":"array","items":{"type":"string"},"maxItems":3}},"required":["query"],"additionalProperties":false}`

func TestValidateSchema(t *testing.T) {
	valid := []string{
		testSchema,
		`{"type":"object","properties":{},"additionalProperties":false}`,
		`{"type":"object","properties":{"nested":{"type":"object","properties":{},"additionalProperties":false}},"additionalProperties":false}`,
	}
	for _, raw := range valid {
		if err := ValidateSchema([]byte(raw)); err != nil { t.Errorf("valid schema rejected: %v", err) }
	}
	invalid := []string{
		`{"type":"object","type":"string","properties":{},"additionalProperties":false}`,
		`{"type":"object","properties":{},"additionalProperties":true}`,
		`{"type":"object","properties":{},"additionalProperties":false,"pattern":"x"}`,
		`{"type":["object","string"],"properties":{},"additionalProperties":false}`,
		`{"type":"object","properties":{"x":{"type":"array"}},"additionalProperties":false}`,
		`{"type":"object","properties":{},"required":["missing"],"additionalProperties":false}`,
		`{"type":"object","properties":{},"required":["x","x"],"additionalProperties":false}`,
		`{"type":"object","properties":{"x":{"type":"integer","enum":[1.0]}},"additionalProperties":false}`,
		`{"type":"object","properties":{"x":{"type":"integer","enum":[1e2]}},"additionalProperties":false}`,
		`{"type":"object","properties":{"x":{"type":"string","minLength":3,"maxLength":2}},"additionalProperties":false}`,
	}
	for _, raw := range invalid {
		if err := ValidateSchema([]byte(raw)); err == nil { t.Errorf("invalid schema accepted: %s", raw) }
	}
}

func TestSchemaDepthBoundary(t *testing.T) {
	base := `{"type":"object","properties":{},"additionalProperties":false}`
	build := func(depth int) string {
		raw := base
		for i := 1; i < depth; i++ {
			raw = `{"type":"object","properties":{"nested":` + raw + `},"additionalProperties":false}`
		}
		return raw
	}
	if err := ValidateSchema([]byte(build(MaxSchemaDepth))); err != nil {
		t.Fatalf("schema at maximum depth should pass: %v", err)
	}
	if err := ValidateSchema([]byte(build(MaxSchemaDepth + 1))); err == nil {
		t.Fatal("schema beyond maximum depth should fail")
	}
}

func TestValidateArguments(t *testing.T) {
	valid := []string{
		`{"query":"Go","limit":2,"enabled":true,"tags":["a","b"]}`,
		`{"query":"x"}`,
	}
	for _, raw := range valid {
		if err := ValidateArguments([]byte(testSchema), []byte(raw)); err != nil { t.Errorf("valid arguments rejected: %v", err) }
	}
	invalid := []string{
		`{"query":""}`,
		`{"limit":1}`,
		`{"query":"x","unknown":true}`,
		`{"query":"x","limit":1.0}`,
		`{"query":"x","limit":1e0}`,
		`{"query":"x","tags":["1","2","3","4"]}`,
		`{"query":"x","query":"y"}`,
		`{"query":"x"} {}`,
	}
	for _, raw := range invalid {
		if err := ValidateArguments([]byte(testSchema), []byte(raw)); err == nil { t.Errorf("invalid arguments accepted: %s", raw) }
	}
}

func TestStrictJSONLimitsAndUTF8(t *testing.T) {
	if _, err := parseStrictJSON([]byte(`{"a":{"b":1}}`), 100, 2); err != nil { t.Fatal(err) }
	if _, err := parseStrictJSON([]byte(`{"a":{"b":1}}`), 100, 1); err == nil { t.Fatal("expected depth error") }
	if _, err := parseStrictJSON([]byte{0xff}, 100, 4); err == nil { t.Fatal("expected UTF-8 error") }
	if _, err := parseStrictJSON([]byte(`{"x":1} {"y":2}`), 100, 4); err == nil { t.Fatal("expected trailing JSON error") }
	if _, err := parseStrictJSON([]byte(`{"x":1,"x":2}`), 100, 4); err == nil { t.Fatal("expected duplicate key error") }
}

func TestStringLengthUsesUnicodeCodePoints(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"value":{"type":"string","minLength":2,"maxLength":2}},"required":["value"],"additionalProperties":false}`)
	if err := ValidateArguments(schema, []byte(`{"value":"é中"}`)); err != nil {
		t.Fatalf("two Unicode code points should pass: %v", err)
	}
	if err := ValidateArguments(schema, []byte(`{"value":"é"}`)); err == nil {
		t.Fatal("one Unicode code point should fail minLength=2")
	}
}

func TestSchemaAndArgumentByteLimits(t *testing.T) {
	if err := ValidateSchema([]byte(strings.Repeat(" ", MaxSchemaBytes+1))); err == nil {
		t.Fatal("schema exceeding byte limit should fail")
	}
	schema := []byte(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`)
	arguments := []byte(`{"value":"` + strings.Repeat("a", MaxArgumentBytes) + `"}`)
	if err := ValidateArguments(schema, arguments); err == nil {
		t.Fatal("arguments exceeding byte limit should fail")
	}
}

func TestSchemaErrorClassification(t *testing.T) {
	err := ValidateArguments([]byte(testSchema), []byte(`{"query":3}`))
	if !errors.Is(err, ErrInvalidArguments) { t.Fatalf("expected ErrInvalidArguments, got %v", err) }
}
