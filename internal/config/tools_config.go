package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var toolConfigNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

// ToolModelAllowlist 使用稳定的 Gateway 模型身份授权工具调用。
// 不使用可变的上游 ModelID、Base URL 或 Provider 配置指纹。
type ToolModelAllowlist struct {
	ProviderID string `yaml:"provider_id"`
	ModelKey   string `yaml:"model_key"`
}

// SearXNGConfig 是服务端固定搜索端点配置，不接受模型或客户端覆盖。
type SearXNGConfig struct {
	BaseURL               string   `yaml:"base_url"`
	AllowInsecureHTTP     bool     `yaml:"allow_insecure_http"`
	AllowPrivateNetwork   bool     `yaml:"allow_private_network"`
	RequestTimeoutSeconds int      `yaml:"request_timeout_seconds"`
	MaxResults            int      `yaml:"max_results"`
	MaxResponseBytes      int64    `yaml:"max_response_bytes"`
	SafeSearch            *int     `yaml:"safe_search"`
	DefaultLanguage       string   `yaml:"default_language"`
	Categories            []string `yaml:"categories"`
}

// ToolsConfig 是 Gateway 侧全局工具能力开关和静态白名单。
// 默认 Enabled=false、白名单为空；API Key 级权限由后续管理配置任务扩展。
type ToolsConfig struct {
	Enabled       bool                 `yaml:"enabled"`
	AllowedModels []ToolModelAllowlist `yaml:"allowed_models"`
	AllowedTools  []string             `yaml:"allowed_tools"`
	SearXNG       SearXNGConfig        `yaml:"searxng"`
}

func (c *ToolsConfig) Normalize() error {
	if c == nil {
		return nil
	}
	c.SearXNG.BaseURL = strings.TrimSpace(c.SearXNG.BaseURL)
	if strings.Contains(c.SearXNG.BaseURL, "${") {
		return fmt.Errorf("tools.searxng.base_url contains an unresolved environment placeholder")
	}
	seenModels := make(map[string]struct{}, len(c.AllowedModels))
	for i := range c.AllowedModels {
		item := &c.AllowedModels[i]
		item.ProviderID = strings.ToLower(strings.TrimSpace(item.ProviderID))
		item.ModelKey = strings.TrimSpace(item.ModelKey)
		if !providerIDPattern.MatchString(item.ProviderID) {
			return fmt.Errorf("tools.allowed_models[%d].provider_id is invalid", i)
		}
		if item.ModelKey == "" || strings.Contains(item.ModelKey, "${") || len(item.ModelKey) > 256 || !utf8.ValidString(item.ModelKey) ||
			strings.ContainsAny(item.ModelKey, "\x00\r\n") {
			return fmt.Errorf("tools.allowed_models[%d].model_key is invalid", i)
		}
		key := item.ProviderID + "\x00" + item.ModelKey
		if _, exists := seenModels[key]; exists {
			return fmt.Errorf("tools.allowed_models[%d] duplicates a Provider/model identity", i)
		}
		seenModels[key] = struct{}{}
	}
	seenTools := make(map[string]struct{}, len(c.AllowedTools))
	for i := range c.AllowedTools {
		name := strings.TrimSpace(c.AllowedTools[i])
		if len(name) == 0 || len(name) > 128 || !toolConfigNamePattern.MatchString(name) {
			return fmt.Errorf("tools.allowed_tools[%d] is invalid", i)
		}
		if _, exists := seenTools[name]; exists {
			return fmt.Errorf("tools.allowed_tools[%d] duplicates tool %q", i, name)
		}
		seenTools[name] = struct{}{}
		c.AllowedTools[i] = name
	}
	if !c.Enabled {
		return nil
	}
	if len(c.AllowedModels) == 0 {
		return fmt.Errorf("tools.allowed_models must not be empty when tools.enabled is true")
	}
	if len(c.AllowedTools) == 0 {
		return fmt.Errorf("tools.allowed_tools must not be empty when tools.enabled is true")
	}
	if _, usesSearch := seenTools["web.search"]; usesSearch {
		if c.SearXNG.BaseURL == "" {
			return fmt.Errorf("tools.searxng.base_url is required when web.search is enabled")
		}
		u, err := url.Parse(c.SearXNG.BaseURL)
		if err != nil || u == nil || !u.IsAbs() || u.Hostname() == "" ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			(u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("tools.searxng.base_url must be an absolute HTTP(S) URL without userinfo, query, or fragment")
		}
		if u.Scheme == "http" && !c.SearXNG.AllowInsecureHTTP {
			return fmt.Errorf("tools.searxng.allow_insecure_http must be true for an HTTP base_url")
		}
	}
	if c.SearXNG.RequestTimeoutSeconds < 0 || c.SearXNG.RequestTimeoutSeconds > 30 {
		return fmt.Errorf("tools.searxng.request_timeout_seconds must be between 0 and 30")
	}
	if c.SearXNG.MaxResults < 0 || c.SearXNG.MaxResults > 5 {
		return fmt.Errorf("tools.searxng.max_results must be between 0 and 5")
	}
	if c.SearXNG.MaxResponseBytes < 0 || c.SearXNG.MaxResponseBytes > 4*1024*1024 {
		return fmt.Errorf("tools.searxng.max_response_bytes must be between 0 and 4194304")
	}
	if c.SearXNG.SafeSearch != nil && (*c.SearXNG.SafeSearch < 0 || *c.SearXNG.SafeSearch > 2) {
		return fmt.Errorf("tools.searxng.safe_search must be 0, 1, or 2")
	}
	return nil
}
