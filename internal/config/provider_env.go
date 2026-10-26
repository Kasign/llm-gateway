package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// applyProviderEnvironment 通过 LLM_GATEWAY_PROVIDER_<ID>_<FIELD> 预置多个只读部署 Provider。
// YAML 与环境变量来源的 ID 冲突交由统一配置校验拒绝。
var providerEnvironmentIDPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,63}$`)

func applyProviderEnvironment(c *Config) error {
	if c == nil { return fmt.Errorf("config is required") }
	const prefix = "LLM_GATEWAY_PROVIDER_"
	fields := []string{"MODEL_MAPPINGS_JSON", "MAX_CONCURRENT_REQUESTS", "TOKEN_BUDGET_PER_DAY", "REQUESTS_PER_MINUTE", "REQUEST_TIMEOUT_SECONDS", "ALLOW_PRIVATE_NETWORK", "ALLOW_INSECURE_HTTP", "MAX_RESPONSE_BYTES", "API_KEY_HEADER", "DEFAULT_MODEL", "BASE_URL", "API_KEY", "ENABLED"}
	values := make(map[string]map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(key, prefix) { continue }
		suffix := strings.TrimPrefix(key, prefix)
		for _, field := range fields {
			marker := "_" + field
			if !strings.HasSuffix(suffix, marker) { continue }
			id := strings.TrimSuffix(suffix, marker)
			if !providerEnvironmentIDPattern.MatchString(id) { return fmt.Errorf("environment variable %s has an invalid Provider ID", key) }
			if values[id] == nil { values[id] = make(map[string]string) }
			values[id][field] = value
			break
		}
	}
	ids := make([]string, 0, len(values))
	for id := range values { ids = append(ids, id) }
	sort.Strings(ids)
	for _, envID := range ids {
		v := values[envID]
		item := OpenAICompatibleProviderConfig{ID: strings.ToLower(envID), Enabled: true, APIKeyHeader: "Authorization", ModelMappings: make(map[string]string)}
		if raw, ok := v["ENABLED"]; ok {
			enabled, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil { return fmt.Errorf("environment Provider %s ENABLED must be a boolean", item.ID) }
			item.Enabled = enabled
		}
		item.BaseURL = strings.TrimSpace(v["BASE_URL"])
		item.APIKey = strings.TrimSpace(v["API_KEY"])
		if header := strings.TrimSpace(v["API_KEY_HEADER"]); header != "" { item.APIKeyHeader = header }
		item.DefaultModel = strings.TrimSpace(v["DEFAULT_MODEL"])
		if raw, ok := v["ALLOW_INSECURE_HTTP"]; ok {
			value, err := strconv.ParseBool(strings.TrimSpace(raw)); if err != nil { return fmt.Errorf("environment Provider %s ALLOW_INSECURE_HTTP must be a boolean", item.ID) }; item.AllowInsecureHTTP = value
		}
		if raw, ok := v["ALLOW_PRIVATE_NETWORK"]; ok {
			value, err := strconv.ParseBool(strings.TrimSpace(raw)); if err != nil { return fmt.Errorf("environment Provider %s ALLOW_PRIVATE_NETWORK must be a boolean", item.ID) }; item.AllowPrivateNetwork = value
		}
		if raw, ok := v["MAX_RESPONSE_BYTES"]; ok && strings.TrimSpace(raw) != "" {
			value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); if err != nil { return fmt.Errorf("environment Provider %s MAX_RESPONSE_BYTES must be an integer", item.ID) }; item.MaxResponseBytes = value
		}
		if raw, ok := v["MAX_CONCURRENT_REQUESTS"]; ok && strings.TrimSpace(raw) != "" {
			value, err := strconv.Atoi(strings.TrimSpace(raw)); if err != nil { return fmt.Errorf("environment Provider %s MAX_CONCURRENT_REQUESTS must be an integer", item.ID) }; item.MaxConcurrentRequests = value
		}
		if raw, ok := v["REQUESTS_PER_MINUTE"]; ok && strings.TrimSpace(raw) != "" {
			value, err := strconv.Atoi(strings.TrimSpace(raw)); if err != nil { return fmt.Errorf("environment Provider %s REQUESTS_PER_MINUTE must be an integer", item.ID) }; item.RequestsPerMinute = value
		}
		if raw, ok := v["TOKEN_BUDGET_PER_DAY"]; ok && strings.TrimSpace(raw) != "" {
			value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); if err != nil { return fmt.Errorf("environment Provider %s TOKEN_BUDGET_PER_DAY must be an integer", item.ID) }; item.TokenBudgetPerDay = value
		}
		if raw, ok := v["REQUEST_TIMEOUT_SECONDS"]; ok && strings.TrimSpace(raw) != "" {
			value, err := strconv.Atoi(strings.TrimSpace(raw)); if err != nil { return fmt.Errorf("environment Provider %s REQUEST_TIMEOUT_SECONDS must be an integer", item.ID) }; item.RequestTimeoutSeconds = value
		}
		if raw := strings.TrimSpace(v["MODEL_MAPPINGS_JSON"]); raw != "" {
			if err := json.Unmarshal([]byte(raw), &item.ModelMappings); err != nil || item.ModelMappings == nil { return fmt.Errorf("environment Provider %s MODEL_MAPPINGS_JSON must be a JSON object", item.ID) }
		}
		c.Providers.OpenAICompatible = append(c.Providers.OpenAICompatible, item)
	}
	return nil
}
