// config 负责从 YAML（及环境变量展开）加载网关运行参数。
// 包括监听地址、数据库、Redis、上游 llama.cpp/Ollama、模型目录与管理员密钥等。
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/whl/llm-gateway/internal/adminuser"
)

// Config 是 config.yaml 在内存中的结构；各字段对应部署时需要的连接与行为开关。
type Config struct {
	Server struct {
		Host           string   `yaml:"host"`
		Port           int      `yaml:"port"`
		TrustedProxies []string `yaml:"trusted_proxies"`
	} `yaml:"server"`
	Auth struct {
		AdminAPIKey       string `yaml:"admin_api_key"`
		AdminUsername     string `yaml:"admin_username"`
		AdminPassword     string `yaml:"admin_password"`      // 仅环境变量注入，启动后哈希并清零
		AdminPasswordHash  string `yaml:"admin_password_hash"` // 推荐：仅存 PBKDF2 哈希
		WebPasswordEnabled bool   `yaml:"web_password_enabled"` // 仅作数据库尚无设置时的初始开关
		SessionTTLHours    int    `yaml:"session_ttl_hours"`
	} `yaml:"auth"`
	Database struct {
		// Host/Port are used to build the application DSN safely.
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		Name     string `yaml:"name"`
		User     string `yaml:"user"`
		Password string `yaml:"password"`

		// Admin credentials are only used for first-start database/user bootstrap.
		// They should point to an existing MariaDB account with CREATE USER/GRANT privileges.
		AdminUser     string `yaml:"admin_user"`
		AdminPassword string `yaml:"admin_password"`
		AutoInit      bool   `yaml:"auto_init"`

		MigrationFile string `yaml:"migration_file"`
	} `yaml:"database"`
	Redis struct {
		Addr     string `yaml:"addr"`
		Password string `yaml:"password"`
		DB       int    `yaml:"db"`
	} `yaml:"redis"`
	Llama struct {
		Backend           string `yaml:"backend"`
		BaseURL           string `yaml:"-"` // 当前生效上游的地址，由两个 base URL 算出，不从文件读取
		LlamaCppBaseURL   string `yaml:"llamacpp_base_url"`
		OllamaBaseURL     string `yaml:"ollama_base_url"`
		UpstreamAPIKey    string `yaml:"upstream_api_key"`
		ModelName         string `yaml:"-"` // 运行时由数据库里的当前模型写入
		ModelsDir         string `yaml:"-"` // 空则用 storage.models_dir
		RequestTimeout        int  `yaml:"-"` // 运行时值；由 RequestTimeoutSeconds 规范化后写入
		RequestTimeoutSeconds *int `yaml:"request_timeout_seconds"` // nil 表示旧配置未指定；显式 0 表示不限整体请求时长
		ModelsMax         int    `yaml:"models_max"`
		AutoLoadOnProxy   bool   `yaml:"auto_load_on_proxy"`
		WarmStart         bool   `yaml:"warm_start"`
		WarmStartComment  string `yaml:"warm_start_comment"`
		WarmStartInterval int    `yaml:"warm_start_interval_seconds"`
		MaxOutputTokens   int    `yaml:"max_output_tokens"`
	} `yaml:"llama"`
	Storage struct {
		ModelsDir string `yaml:"models_dir"`
	} `yaml:"storage"`
	Providers struct {
		OpenAICompatible []OpenAICompatibleProviderConfig `yaml:"openai_compatible"`
	} `yaml:"providers"`
	Agents []AgentRouteConfig `yaml:"agents"`
	Download struct {
		HFBaseURL        string `yaml:"hf_base_url"`
		OllamaLibraryURL string `yaml:"ollama_library_url"`
		Timeout          int    `yaml:"timeout_seconds"`
		// CatalogTimeout limits admin model search (HF + Ollama library HTML).
		CatalogTimeout int    `yaml:"catalog_timeout_seconds"`
		UserAgent      string `yaml:"user_agent"`
	} `yaml:"download"`
	Logging struct {
		Enabled            *bool `yaml:"enabled"`
		Verbose            bool  `yaml:"verbose"`
		StorePayload       bool  `yaml:"store_payload"`
		StoreStreamRaw     bool  `yaml:"store_stream_raw"`
		StoreStreamPreview       bool `yaml:"store_stream_preview"`
		RetentionDays            int  `yaml:"retention_days"`
		RetentionBatchSize       int  `yaml:"retention_batch_size"`
		RetentionIntervalSeconds int  `yaml:"retention_interval_seconds"`
	} `yaml:"logging"`
}

// LoggingEnabled 未显式关闭时默认开启应用日志，便于排查线上问题。
func (c *Config) LoggingEnabled() bool {
	if c.Logging.Enabled == nil {
		return true
	}
	return *c.Logging.Enabled
}

// WarmStartEnabled 为真时，流式推理在等上游首包前先写一行 SSE 注释。默认关闭。
func (c *Config) WarmStartEnabled() bool {
	return c != nil && c.Llama.WarmStart
}

// WarmStartComment 是 SSE 注释正文，不含冒号。空则用 keepalive。
func (c *Config) WarmStartComment() string {
	if c == nil {
		return "keepalive"
	}
	s := strings.TrimSpace(c.Llama.WarmStartComment)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if s == "" {
		return "keepalive"
	}
	return s
}

// WarmStartInterval 是重复写保活注释的间隔。未配置或 <=0 时用 15 秒，低于常见的 60 秒读空闲超时。
// warm_start 关闭时返回 0，调用方不要启动重复写。
func (c *Config) WarmStartInterval() time.Duration {
	if c == nil || !c.WarmStartEnabled() {
		return 0
	}
	sec := c.Llama.WarmStartInterval
	if sec <= 0 {
		sec = 15
	}
	return time.Duration(sec) * time.Second
}

// IsOllama 判断当前配置的主后端是否为 Ollama（否则按 llama.cpp 处理）。
func (c *Config) IsOllama() bool {
	return strings.EqualFold(c.Llama.Backend, "ollama")
}

// BaseURLForBackend 按后端类型返回上游 HTTP 地址（来自配置或 .env，已去掉末尾斜杠）。
// BaseURLForBackend returns the upstream URL for llamacpp or ollama from config / .env.
func (c *Config) BaseURLForBackend(backend string) string {
	backend = strings.ToLower(strings.TrimSpace(backend))
	if backend == "" {
		backend = "llamacpp"
	}
	pick := func(raw string) string {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.Contains(raw, "${") {
			return ""
		}
		return strings.TrimRight(raw, "/")
	}
	switch backend {
	case "ollama":
		return pick(c.Llama.OllamaBaseURL)
	default:
		return pick(c.Llama.LlamaCppBaseURL)
	}
}

// validateLocalInferenceBaseURL 防止本地控制客户端通过 URL userinfo/query 承载并泄漏凭据。
// 空值允许存在于非活动后端；活动后端是否已配置由 Load 后续统一检查。
func validateLocalInferenceBaseURL(field, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || !u.IsAbs() || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.RawFragment != "" || strings.Contains(raw, "#") || u.Opaque != "" {
		return fmt.Errorf("config field %s must be an absolute HTTP(S) URL without userinfo, query, or fragment", field)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("config field %s must use HTTP or HTTPS", field)
	}
	if strings.Contains(u.Host, "\\") || strings.Contains(u.Path, "\\") {
		return fmt.Errorf("config field %s contains an invalid URL path", field)
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("config field %s must not contain URL dot segments", field)
		}
	}
	return nil
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// 仅展开 ${VAR}，避免 os.ExpandEnv 破坏 pbkdf2 哈希中的 $ 片段。
var configEnvRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func expandConfigEnv(raw string) string {
	// 先解析 YAML，再替换字符串标量，最后让 YAML encoder 负责转义引号、反斜杠、冒号和换行。
	// 不能在原始文本中直接插入 secret，否则含引号/换行的环境变量可能破坏配置结构。
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &document); err != nil {
		// 保留原文，让 Config.Load 的正式解析路径返回带上下文的 YAML 错误。
		return raw
	}
	expandConfigEnvNode(&document)
	expanded, err := yaml.Marshal(&document)
	if err != nil {
		return raw
	}
	return string(expanded)
}

// inferredConfigEnvScalarTag 只恢复 YAML 的基本标量类型，不把复杂 YAML 片段解释为结构节点。
func inferredConfigEnvScalarTag(value string) string {
	if strings.TrimSpace(value) == "" {
		return "!!str"
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(value), &document); err != nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return "!!str"
	}
	node := document.Content[0]
	if node.Kind != yaml.ScalarNode {
		return "!!str"
	}
	switch node.Tag {
	case "!!int", "!!float", "!!bool", "!!null":
		return node.Tag
	default:
		return "!!str"
	}
}

func expandConfigEnvNode(node *yaml.Node) {
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			expandConfigEnvNode(child)
		}
	case yaml.MappingNode:
		// MappingNode 的 Content 按 key/value 成对排列。只展开 value，配置键名保持固定。
		for i := 1; i < len(node.Content); i += 2 {
			expandConfigEnvNode(node.Content[i])
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			expandConfigEnvNode(child)
		}
	case yaml.ScalarNode:
		if node.Tag != "!!str" {
			return
		}
		original := node.Value
		// 只有未加引号、且整个标量都是一个环境变量时，才根据变量值恢复 YAML 原生标量类型。
		// 例如 request_timeout_seconds: ${VAR} 在 VAR=0 时必须变成 !!int 0，而不是 !!str "0"。
		// 带引号的环境变量始终按字符串处理，避免 API Key、密码哈希和数字型用户名被错误转换。
		if node.Style == 0 && configEnvRef.MatchString(original) && configEnvRef.FindString(original) == original {
			name := original[2 : len(original)-1]
			if value, ok := os.LookupEnv(name); ok {
				node.Value = value
				if tag := inferredConfigEnvScalarTag(value); tag != "" {
					node.Tag = tag
				}
				return
			}
		}
		node.Value = configEnvRef.ReplaceAllStringFunc(original, func(m string) string {
			name := m[2 : len(m)-1]
			if value, ok := os.LookupEnv(name); ok {
				return value
			}
			// 保留占位符，后续字段级校验会拒绝仍未解析的必需配置。
			return m
		})
	}
}

// clearUnresolvedPlaceholder 清除允许回退/可选的字段中的未解析占位符，避免把占位字符串当作真实配置使用。
func clearUnresolvedPlaceholder(value *string) {
	if value != nil && strings.Contains(*value, "${") {
		*value = ""
	}
}

// validateResolvedConfigEnv 不打印配置值或密钥，只报告尚未展开的字段路径。
// 禁用的远程 Provider 不参与运行，可保留其未配置的 URL/Key；启用的 Provider 必须全部解析成功。
func validateResolvedConfigEnv(c *Config) error {
	check := func(field, value string) error {
		if strings.Contains(value, "${") {
			return fmt.Errorf("config field %s contains an unresolved environment variable placeholder", field)
		}
		return nil
	}
	checks := []struct {
		field string
		value string
	}{
		{"server.host", c.Server.Host},
		{"auth.admin_api_key", c.Auth.AdminAPIKey},
		{"auth.admin_username", c.Auth.AdminUsername},
		{"auth.admin_password", c.Auth.AdminPassword},
		{"auth.admin_password_hash", c.Auth.AdminPasswordHash},
		{"database.host", c.Database.Host},
		{"database.name", c.Database.Name},
		{"database.user", c.Database.User},
		{"database.password", c.Database.Password},
		{"database.migration_file", c.Database.MigrationFile},
		{"redis.addr", c.Redis.Addr},
		{"redis.password", c.Redis.Password},
		{"llama.backend", c.Llama.Backend},
		{"llama.llamacpp_base_url", c.Llama.LlamaCppBaseURL},
		{"llama.ollama_base_url", c.Llama.OllamaBaseURL},
		{"llama.upstream_api_key", c.Llama.UpstreamAPIKey},
		{"llama.warm_start_comment", c.Llama.WarmStartComment},
		{"storage.models_dir", c.Storage.ModelsDir},
		{"download.hf_base_url", c.Download.HFBaseURL},
		{"download.ollama_library_url", c.Download.OllamaLibraryURL},
		{"download.user_agent", c.Download.UserAgent},
	}
	for _, item := range checks {
		if err := check(item.field, item.value); err != nil {
			return err
		}
	}
	for i, proxy := range c.Server.TrustedProxies {
		if err := check(fmt.Sprintf("server.trusted_proxies[%d]", i), proxy); err != nil {
			return err
		}
	}
	if c.Database.AutoInit {
		if err := check("database.admin_user", c.Database.AdminUser); err != nil {
			return err
		}
		if err := check("database.admin_password", c.Database.AdminPassword); err != nil {
			return err
		}
	}
	for i, item := range c.Providers.OpenAICompatible {
		if !item.Enabled {
			continue
		}
		prefix := fmt.Sprintf("providers.openai_compatible[%d].", i)
		for _, field := range []struct{ name, value string }{
			{"id", item.ID}, {"base_url", item.BaseURL}, {"api_key", item.APIKey},
			{"api_key_header", item.APIKeyHeader}, {"default_model", item.DefaultModel},
		} {
			if err := check(prefix+field.name, field.value); err != nil {
				return err
			}
		}
	}
	return nil
}

// Load 读取并校验配置文件，填充默认值；校验失败会直接返回错误，避免带着错误配置启动。
const (
	defaultLocalRequestTimeoutSeconds = 3600
	maxLocalRequestTimeoutSeconds     = 604800
)

// normalizeLlamaRequestTimeout 保留旧配置的一小时默认值，同时允许新配置显式使用 0 关闭客户端整体超时。
// 即使整体超时关闭，调用方取消请求仍会通过 HTTP 请求上下文中止在途推理。
func (c *Config) normalizeLlamaRequestTimeout() error {
	if c == nil {
		return errors.New("config is required")
	}
	if c.Llama.RequestTimeoutSeconds == nil {
		c.Llama.RequestTimeout = defaultLocalRequestTimeoutSeconds
		return nil
	}
	seconds := *c.Llama.RequestTimeoutSeconds
	if seconds < 0 || seconds > maxLocalRequestTimeoutSeconds {
		return fmt.Errorf("llama.request_timeout_seconds must be between 0 and %d (0 disables the client-wide request timeout)", maxLocalRequestTimeoutSeconds)
	}
	c.Llama.RequestTimeout = seconds
	return nil
}

const (
	defaultRetentionBatchSize       = 500
	maxRetentionBatchSize           = 5000
	defaultRetentionIntervalSeconds = 3600
	minRetentionIntervalSeconds     = 60
	maxRetentionIntervalSeconds     = 86400
	maxRetentionDays                = 36500
)

// normalizeLogRetention 只有明确设置正数保留天数才启用清理；默认永不主动删除历史请求。
func (c *Config) normalizeLogRetention() error {
	if c == nil {
		return errors.New("config is required")
	}
	if c.Logging.RetentionDays < 0 || c.Logging.RetentionDays > maxRetentionDays {
		return fmt.Errorf("logging.retention_days must be between 0 and %d (0 disables automatic cleanup)", maxRetentionDays)
	}
	if c.Logging.RetentionBatchSize <= 0 {
		c.Logging.RetentionBatchSize = defaultRetentionBatchSize
	}
	if c.Logging.RetentionBatchSize > maxRetentionBatchSize {
		return fmt.Errorf("logging.retention_batch_size must not exceed %d", maxRetentionBatchSize)
	}
	if c.Logging.RetentionIntervalSeconds <= 0 {
		c.Logging.RetentionIntervalSeconds = defaultRetentionIntervalSeconds
	}
	if c.Logging.RetentionIntervalSeconds < minRetentionIntervalSeconds || c.Logging.RetentionIntervalSeconds > maxRetentionIntervalSeconds {
		return fmt.Errorf("logging.retention_interval_seconds must be between %d and %d", minRetentionIntervalSeconds, maxRetentionIntervalSeconds)
	}
	return nil
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Allow secrets/paths to be supplied through the environment, e.g.
	// ${MARIADB_ROOT_PASSWORD}. This keeps credentials out of the image.
	data = []byte(expandConfigEnv(string(data)))

	var c Config
	// 旧配置未显式设置时，默认开启 Web 密码保护。
	c.Auth.WebPasswordEnabled = true
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// Web 登录与多 Provider 预置项由环境变量覆盖或补充。
	if err := applyAuthEnv(&c); err != nil {
		return nil, err
	}
	if err := applyProviderEnvironment(&c); err != nil {
		return nil, err
	}
	clearUnresolvedPlaceholder(&c.Auth.AdminUsername)
	clearUnresolvedPlaceholder(&c.Auth.AdminPassword)
	clearUnresolvedPlaceholder(&c.Auth.AdminPasswordHash)
	// 非活动的本地后端可以不配置。活动后端缺少地址会在下面的 BaseURL 检查中失败。
	clearUnresolvedPlaceholder(&c.Llama.LlamaCppBaseURL)
	clearUnresolvedPlaceholder(&c.Llama.OllamaBaseURL)

	if c.Server.Host == "" {
		c.Server.Host = "127.0.0.1"
	}
	if c.Server.Port == 0 {
		c.Server.Port = 8088
	}
	if c.Database.Port == 0 {
		c.Database.Port = 3306
	}
	if c.Database.MigrationFile == "" {
		c.Database.MigrationFile = "/app/migrations/001_init.sql"
	}
	if err := c.normalizeLlamaRequestTimeout(); err != nil {
		return nil, err
	}
	if err := c.normalizeLogRetention(); err != nil {
		return nil, err
	}
	c.Llama.Backend = strings.ToLower(strings.TrimSpace(c.Llama.Backend))
	if c.Llama.Backend == "" || strings.Contains(c.Llama.Backend, "${") {
		c.Llama.Backend = "llamacpp"
	}
	if c.Llama.Backend != "llamacpp" && c.Llama.Backend != "ollama" {
		return nil, fmt.Errorf("llama.backend must be llamacpp or ollama, got %q", c.Llama.Backend)
	}
	if strings.Contains(c.Llama.ModelName, "${") {
		c.Llama.ModelName = ""
	}
	c.Llama.ModelName = strings.TrimSpace(c.Llama.ModelName)
	if c.Llama.MaxOutputTokens <= 0 {
		c.Llama.MaxOutputTokens = 8192
	}
	if c.Llama.ModelsDir == "" || strings.Contains(c.Llama.ModelsDir, "${") {
		c.Llama.ModelsDir = c.Storage.ModelsDir
	}
	if c.Download.Timeout <= 0 {
		c.Download.Timeout = 3600
	}
	if c.Download.HFBaseURL == "" {
		c.Download.HFBaseURL = "https://huggingface.co"
	}
	if c.Download.OllamaLibraryURL == "" {
		c.Download.OllamaLibraryURL = "https://ollama.com"
	}
	if c.Download.UserAgent == "" {
		c.Download.UserAgent = "llm-gateway/3.0"
	}
	if c.Download.CatalogTimeout <= 0 {
		c.Download.CatalogTimeout = 60
	}
	if c.Storage.ModelsDir == "" {
		return nil, errors.New("storage.models_dir is required")
	}
	for _, item := range []struct {
		field string
		value string
	}{
		{"llama.llamacpp_base_url", c.Llama.LlamaCppBaseURL},
		{"llama.ollama_base_url", c.Llama.OllamaBaseURL},
	} {
		if err := validateLocalInferenceBaseURL(item.field, item.value); err != nil {
			return nil, err
		}
	}
	llamaCppURL := c.BaseURLForBackend("llamacpp")
	ollamaURL := c.BaseURLForBackend("ollama")
	if ollamaURL != "" && ollamaBaseURLIsPublicLibrary(ollamaURL) {
		return nil, fmt.Errorf(
			"OLLAMA_BASE_URL 不能指向公网模型库 %s；本机推理请填如 http://127.0.0.1:11434，公网浏览/下载在 download.ollama_library_url",
			ollamaURL,
		)
	}
	if ollamaURL != "" && llamaCppURL != "" && ollamaURL == llamaCppURL {
		return nil, fmt.Errorf(
			"OLLAMA_BASE_URL 不能与 LLAMA_CPP_BASE_URL 相同 (%s)；Ollama 需独立地址如 http://ollama:11434",
			ollamaURL,
		)
	}
	if llamaCppURL == "" && ollamaURL == "" {
		return nil, errors.New("no upstream URL: set at least LLAMA_CPP_BASE_URL or OLLAMA_BASE_URL in the repository-root .env file")
	}
	c.Llama.BaseURL = c.BaseURLForBackend(c.Llama.Backend)
	if c.Llama.BaseURL == "" {
		if c.Llama.Backend == "ollama" {
			return nil, fmt.Errorf("active backend is ollama but OLLAMA_BASE_URL is missing or unresolved in the repository-root .env file")
		}
		return nil, fmt.Errorf("active backend is llamacpp but LLAMA_CPP_BASE_URL is missing or unresolved in the repository-root .env file")
	}
	if strings.Contains(c.Llama.UpstreamAPIKey, "${") {
		c.Llama.UpstreamAPIKey = ""
	}
	c.Llama.UpstreamAPIKey = strings.TrimSpace(c.Llama.UpstreamAPIKey)
	if err := validateResolvedConfigEnv(&c); err != nil {
		return nil, err
	}
	if c.Database.Host == "" {
		return nil, errors.New("database.host is required")
	}
	if c.Database.Name == "" {
		c.Database.Name = "llm_gateway"
	}
	if c.Database.User == "" {
		c.Database.User = "llm_gateway"
	}
	if !identifierPattern.MatchString(c.Database.Name) {
		return nil, errors.New("database.name contains invalid characters")
	}
	if !identifierPattern.MatchString(c.Database.User) {
		return nil, errors.New("database.user contains invalid characters")
	}
	if c.Database.AutoInit && (c.Database.AdminUser == "" || c.Database.AdminPassword == "") {
		return nil, errors.New("database.admin_user and database.admin_password are required when auto_init is enabled")
	}
	if c.Database.Password == "" {
		return nil, errors.New("database.password is required")
	}
	if c.Auth.AdminAPIKey == "" || c.Auth.AdminAPIKey == "CHANGE_ME_TO_A_LONG_RANDOM_ADMIN_KEY" {
		return nil, errors.New("auth.admin_api_key must be changed from the example value")
	}
	if err := c.validateProviderConfigs(); err != nil {
		return nil, err
	}
	if err := adminuser.NormalizeWebLogin(&c.Auth.AdminUsername, &c.Auth.AdminPassword, &c.Auth.AdminPasswordHash); err != nil {
		return nil, fmt.Errorf("auth web login: %w", err)
	}
	return &c, nil
}

// applyAuthEnv 直接从进程环境读取 Web 登录项，避免 YAML 展开或 compose 未注入 ${} 时为空。
func applyAuthEnv(c *Config) error {
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ADMIN_USERNAME")); v != "" && !strings.Contains(v, "${") {
		c.Auth.AdminUsername = v
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ADMIN_PASSWORD_HASH")); looksLikePasswordHash(v) {
		c.Auth.AdminPasswordHash = v
	}
	if v := os.Getenv("LLM_GATEWAY_ADMIN_PASSWORD"); v != "" && !strings.Contains(v, "${") {
		c.Auth.AdminPassword = v
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_WEB_PASSWORD_ENABLED")); v != "" {
		enabled, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("auth.web_password_enabled must be a boolean")
		}
		c.Auth.WebPasswordEnabled = enabled
	}
	return nil
}

func looksLikePasswordHash(v string) bool {
	v = strings.TrimSpace(v)
	if len(v) <= 40 {
		return false
	}
	if strings.HasPrefix(v, "pbkdf2$") {
		return strings.Count(v, "$") >= 3
	}
	if strings.HasPrefix(v, "pbkdf2:") {
		return strings.Count(v, ":") >= 3
	}
	return false
}

// 与 llama.UpstreamURLIsOllamaLibrary 保持一致：公网库域名不能作 OLLAMA_BASE_URL。
func ollamaBaseURLIsPublicLibrary(baseURL string) bool {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "ollama.com", "www.ollama.com":
		return true
	default:
		return false
	}
}
