package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var toolConfigNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

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

// ToolsConfig 只保存部署级工具开关、工具目录和固定执行器配置。
// 模型与工具的调用授权统一通过 Web 服务密钥的 allowed_models / allowed_tools 管理。
type ToolsConfig struct {
	Enabled      bool          `yaml:"enabled"`
	AllowedTools []string      `yaml:"allowed_tools"`
	SearXNG      SearXNGConfig `yaml:"searxng"`
}

func (c *ToolsConfig) Normalize() error {
	if c == nil {
		return nil
	}
	c.SearXNG.BaseURL = strings.TrimSpace(c.SearXNG.BaseURL)
	if strings.Contains(c.SearXNG.BaseURL, "${") {
		return fmt.Errorf("tools.searxng.base_url contains an unresolved environment placeholder")
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
