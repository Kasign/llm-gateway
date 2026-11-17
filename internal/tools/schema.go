package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"unicode/utf8"
)

const (
	MaxToolNameBytes       = 128
	MaxToolDescriptionBytes = 1024
	MaxSchemaBytes         = 16 * 1024
	MaxArgumentBytes       = 8 * 1024
	MaxSchemaDepth         = 8
	MaxArgumentDepth       = 16
	MaxObjectProperties    = 64
	MaxEnumItems            = 128
	MaxArrayItems           = 128
	MaxRegistryTools        = 64
)

var (
	ErrInvalidSchema    = errors.New("invalid tool schema")
	ErrInvalidArguments = errors.New("invalid tool arguments")
	toolNamePattern     = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
)

type jsonKind uint8

const (
	jsonNull jsonKind = iota
	jsonBool
	jsonNumber
	jsonString
	jsonArray
	jsonObject
)

type jsonNode struct {
	kind jsonKind
	b    bool
	s    string
	a    []*jsonNode
	o    map[string]*jsonNode
}

// parseStrictJSON 在解码时保留对象键和数字字面值，避免 map 解码丢失重复键证据。
func parseStrictJSON(raw []byte, maxBytes, maxDepth int) (*jsonNode, error) {
	if len(raw) == 0 || len(raw) > maxBytes {
		return nil, fmt.Errorf("JSON byte length must be between 1 and %d", maxBytes)
	}
	if !utf8.Valid(raw) {
		return nil, errors.New("JSON contains invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	root, err := parseJSONNode(dec, 1, maxDepth)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, errors.New("JSON contains a trailing value")
		}
		return nil, fmt.Errorf("invalid trailing JSON: %w", err)
	}
	return root, nil
}

func parseJSONNode(dec *json.Decoder, depth, maxDepth int) (*jsonNode, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("JSON nesting exceeds maximum depth %d", maxDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch v := tok.(type) {
	case nil:
		return &jsonNode{kind: jsonNull}, nil
	case bool:
		return &jsonNode{kind: jsonBool, b: v}, nil
	case string:
		return &jsonNode{kind: jsonString, s: v}, nil
	case json.Number:
		return &jsonNode{kind: jsonNumber, s: v.String()}, nil
	case json.Delim:
		switch v {
		case '{':
			n := &jsonNode{kind: jsonObject, o: make(map[string]*jsonNode)}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				if _, exists := n.o[key]; exists {
					return nil, fmt.Errorf("duplicate object key %q", key)
				}
				value, err := parseJSONNode(dec, depth+1, maxDepth)
				if err != nil {
					return nil, err
				}
				n.o[key] = value
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errors.New("unterminated JSON object")
			}
			return n, nil
		case '[':
			n := &jsonNode{kind: jsonArray}
			for dec.More() {
				value, err := parseJSONNode(dec, depth+1, maxDepth)
				if err != nil {
					return nil, err
				}
				n.a = append(n.a, value)
			}
			end, err := dec.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errors.New("unterminated JSON array")
			}
			return n, nil
		}
	}
	return nil, fmt.Errorf("unsupported JSON token %v", tok)
}

// ValidateSchema 检查第一版支持的 JSON Schema 子集。
func ValidateSchema(raw []byte) error {
	// Schema 原文包含 properties 容器和关键字值；JSON 语法树深度上限
	// 与 Schema 节点深度分开计算，真正的 Schema 深度由 validateSchemaNode 限制。
	root, err := parseStrictJSON(raw, MaxSchemaBytes, MaxSchemaDepth*3+4)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	if root.kind != jsonObject {
		return fmt.Errorf("%w: root must be an object", ErrInvalidSchema)
	}
	if err := validateSchemaNode(root, 1, true); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSchema, err)
	}
	return nil
}

var schemaKeywords = map[string]struct{}{
	"type": {}, "description": {}, "enum": {},
	"properties": {}, "required": {}, "additionalProperties": {},
	"items": {}, "minItems": {}, "maxItems": {},
	"minLength": {}, "maxLength": {},
}

func validateSchemaNode(n *jsonNode, depth int, root bool) error {
	if depth > MaxSchemaDepth {
		return fmt.Errorf("schema nesting exceeds maximum depth %d", MaxSchemaDepth)
	}
	if n.kind != jsonObject {
		return errors.New("schema node must be an object")
	}
	for key := range n.o {
		if _, ok := schemaKeywords[key]; !ok {
			return fmt.Errorf("unsupported schema keyword %q", key)
		}
	}
	typ, ok := n.o["type"]
	if !ok || typ.kind != jsonString {
		return errors.New("type must be a string")
	}
	if desc, exists := n.o["description"]; exists && desc.kind != jsonString {
		return errors.New("description must be a string")
	}
	if enum, exists := n.o["enum"]; exists {
		if enum.kind != jsonArray || len(enum.a) == 0 || len(enum.a) > MaxEnumItems {
			return fmt.Errorf("enum must contain 1..%d scalar values", MaxEnumItems)
		}
		for _, item := range enum.a {
			if !scalarMatchesType(item, typ.s) {
				return errors.New("enum values must be scalar values matching type")
			}
		}
	}
	switch typ.s {
	case "object":
		props, exists := n.o["properties"]
		if !exists || props.kind != jsonObject {
			return errors.New("object schema must declare properties object")
		}
		if len(props.o) > MaxObjectProperties {
			return fmt.Errorf("object properties exceed maximum %d", MaxObjectProperties)
		}
		additional, exists := n.o["additionalProperties"]
		if !exists || additional.kind != jsonBool || additional.b {
			return errors.New("object schema must set additionalProperties to false")
		}
		if _, exists := n.o["items"]; exists || hasAny(n, "minItems", "maxItems", "minLength", "maxLength") {
			return errors.New("object schema contains keywords for another type")
		}
		if required, exists := n.o["required"]; exists {
			if required.kind != jsonArray {
				return errors.New("required must be an array of property names")
			}
			seen := make(map[string]struct{}, len(required.a))
			for _, item := range required.a {
				if item.kind != jsonString {
					return errors.New("required entries must be strings")
				}
				if _, duplicate := seen[item.s]; duplicate {
					return fmt.Errorf("required contains duplicate property %q", item.s)
				}
				if _, found := props.o[item.s]; !found {
					return fmt.Errorf("required property %q is not declared", item.s)
				}
				seen[item.s] = struct{}{}
			}
		}
		for name, child := range props.o {
			if err := validateSchemaNode(child, depth+1, false); err != nil {
				return fmt.Errorf("property %q: %w", name, err)
			}
		}
	case "array":
		items, exists := n.o["items"]
		if !exists {
			return errors.New("array schema must declare items")
		}
		if hasAny(n, "properties", "required", "additionalProperties", "minLength", "maxLength") {
			return errors.New("array schema contains keywords for another type")
		}
		if err := validateSchemaNode(items, depth+1, false); err != nil {
			return fmt.Errorf("items: %w", err)
		}
		min, minOK, err := optionalNonNegativeInt(n, "minItems", MaxArrayItems)
		if err != nil { return err }
		max, maxOK, err := optionalNonNegativeInt(n, "maxItems", MaxArrayItems)
		if err != nil { return err }
		if minOK && maxOK && min > max {
			return errors.New("minItems must not exceed maxItems")
		}
	case "string":
		if hasAny(n, "properties", "required", "additionalProperties", "items", "minItems", "maxItems") {
			return errors.New("string schema contains keywords for another type")
		}
		min, minOK, err := optionalNonNegativeInt(n, "minLength", math.MaxInt64)
		if err != nil { return err }
		max, maxOK, err := optionalNonNegativeInt(n, "maxLength", math.MaxInt64)
		if err != nil { return err }
		if minOK && maxOK && min > max {
			return errors.New("minLength must not exceed maxLength")
		}
	case "integer", "boolean":
		if hasAny(n, "properties", "required", "additionalProperties", "items", "minItems", "maxItems", "minLength", "maxLength") {
			return fmt.Errorf("%s schema contains unsupported constraints", typ.s)
		}
	default:
		return fmt.Errorf("unsupported schema type %q", typ.s)
	}
	if root && typ.s != "object" {
		return errors.New("root schema type must be object")
	}
	return nil
}

func hasAny(n *jsonNode, keys ...string) bool {
	for _, key := range keys {
		if _, ok := n.o[key]; ok { return true }
	}
	return false
}

func optionalNonNegativeInt(n *jsonNode, key string, maximum int64) (int64, bool, error) {
	v, ok := n.o[key]
	if !ok { return 0, false, nil }
	if v.kind != jsonNumber {
		return 0, true, fmt.Errorf("%s must be an integer", key)
	}
	i, err := parseLexicalInt(v.s)
	if err != nil || i < 0 || i > maximum {
		return 0, true, fmt.Errorf("%s must be between 0 and %d", key, maximum)
	}
	return i, true, nil
}

func parseLexicalInt(s string) (int64, error) {
	if s == "" { return 0, errors.New("empty integer") }
	for i, r := range s {
		if i == 0 && r == '-' { continue }
		if r < '0' || r > '9' { return 0, errors.New("number is not lexical integer") }
	}
	return strconv.ParseInt(s, 10, 64)
}

func scalarMatchesType(n *jsonNode, typ string) bool {
	switch typ {
	case "string": return n.kind == jsonString
	case "integer":
		if n.kind != jsonNumber { return false }
		_, err := parseLexicalInt(n.s)
		return err == nil
	case "boolean": return n.kind == jsonBool
	default: return false
	}
}

// ValidateArguments 使用已登记 Schema 的同一子集规则校验模型返回的参数 JSON。
func ValidateArguments(schema, arguments []byte) error {
	if err := ValidateSchema(schema); err != nil { return err }
	rootSchema, err := parseStrictJSON(schema, MaxSchemaBytes, MaxSchemaDepth*3+4)
	if err != nil { return fmt.Errorf("%w: %v", ErrInvalidSchema, err) }
	value, err := parseStrictJSON(arguments, MaxArgumentBytes, MaxArgumentDepth)
	if err != nil { return fmt.Errorf("%w: %v", ErrInvalidArguments, err) }
	if err := validateValue(rootSchema, value, "$", 1); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	return nil
}

func validateValue(schema, value *jsonNode, path string, depth int) error {
	if depth > MaxArgumentDepth {
		return fmt.Errorf("%s exceeds maximum nesting depth %d", path, MaxArgumentDepth)
	}
	typ := schema.o["type"].s
	if !valueMatchesType(value, typ) {
		return fmt.Errorf("%s must be %s", path, typ)
	}
	if enum, ok := schema.o["enum"]; ok {
		matched := false
		for _, item := range enum.a {
			if scalarEqual(item, value) { matched = true; break }
		}
		if !matched { return fmt.Errorf("%s is not an allowed enum value", path) }
	}
	switch typ {
	case "object":
		props := schema.o["properties"].o
		for _, required := range stringArray(schema.o["required"]) {
			if _, ok := value.o[required]; !ok {
				return fmt.Errorf("%s.%s is required", path, required)
			}
		}
		for key, childValue := range value.o {
			childSchema, ok := props[key]
			if !ok { return fmt.Errorf("%s.%s is not allowed", path, key) }
			if err := validateValue(childSchema, childValue, path+"."+key, depth+1); err != nil { return err }
		}
	case "array":
		if len(value.a) > MaxArrayItems { return fmt.Errorf("%s exceeds maximum array length %d", path, MaxArrayItems) }
		if min, ok, _ := optionalNonNegativeInt(schema, "minItems", MaxArrayItems); ok && int64(len(value.a)) < min {
			return fmt.Errorf("%s has fewer than %d items", path, min)
		}
		if max, ok, _ := optionalNonNegativeInt(schema, "maxItems", MaxArrayItems); ok && int64(len(value.a)) > max {
			return fmt.Errorf("%s has more than %d items", path, max)
		}
		for i, item := range value.a {
			if err := validateValue(schema.o["items"], item, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil { return err }
		}
	case "string":
		length := int64(utf8.RuneCountInString(value.s))
		if min, ok, _ := optionalNonNegativeInt(schema, "minLength", math.MaxInt64); ok && length < min {
			return fmt.Errorf("%s is shorter than %d characters", path, min)
		}
		if max, ok, _ := optionalNonNegativeInt(schema, "maxLength", math.MaxInt64); ok && length > max {
			return fmt.Errorf("%s is longer than %d characters", path, max)
		}
	}
	return nil
}

func stringArray(n *jsonNode) []string {
	if n == nil { return nil }
	out := make([]string, 0, len(n.a))
	for _, item := range n.a { out = append(out, item.s) }
	return out
}

func valueMatchesType(n *jsonNode, typ string) bool {
	switch typ {
	case "object": return n.kind == jsonObject
	case "array": return n.kind == jsonArray
	case "string": return n.kind == jsonString
	case "integer": return scalarMatchesType(n, "integer")
	case "boolean": return n.kind == jsonBool
	default: return false
	}
}

func scalarEqual(a, b *jsonNode) bool {
	if a.kind != b.kind { return false }
	switch a.kind {
	case jsonString: return a.s == b.s
	case jsonBool: return a.b == b.b
	case jsonNumber:
		ai, ae := parseLexicalInt(a.s); bi, be := parseLexicalInt(b.s)
		return ae == nil && be == nil && ai == bi
	default: return false
	}
}
