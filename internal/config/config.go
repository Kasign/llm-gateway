// config 负责从 YAML（及环境变量展开）加载网关运行参数。
// 包括监听地址、数据库、Redis、上游 llama.cpp/Ollama、模型目录与管理员密钥等。
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/whl/llm-gateway/internal/adminuser"
)

// Config 是 config.yaml 在内存中的结构；各字段对应部署时需要的连接与行为开关。
type Config struct {
	Server struct {
		Host string `yaml:"host"`
		Port int    `yaml:"port"`
	} `yaml:"server"`
	Auth struct {
		AdminAPIKey       string `yaml:"admin_api_key"`
		AdminUsername     string `yaml:"admin_username"`
		AdminPassword     string `yaml:"admin_password"`      // 仅环境变量注入，启动后哈希并清零
		AdminPasswordHash string `yaml:"admin_password_hash"` // 推荐：仅存 PBKDF2 哈希
		SessionTTLHours   int    `yaml:"session_ttl_hours"`
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
		RequestTimeout    int    `yaml:"request_timeout_seconds"`
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
		StoreStreamPreview bool  `yaml:"store_stream_preview"`
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

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// 仅展开 ${VAR}，避免 os.ExpandEnv 破坏 pbkdf2 哈希中的 $ 片段。
var configEnvRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func expandConfigEnv(raw string) string {
	return configEnvRef.ReplaceAllStringFunc(raw, func(m string) string {
		name := m[2 : len(m)-1]
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return m
	})
}

// Load 读取并校验配置文件，填充默认值；校验失败会直接返回错误，避免带着错误配置启动。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Allow secrets/paths to be supplied through the environment, e.g.
	// ${MARIADB_ROOT_PASSWORD}. This keeps credentials out of the image.
	data = []byte(expandConfigEnv(string(data)))

	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
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
	if c.Llama.RequestTimeout <= 0 {
		c.Llama.RequestTimeout = 3600
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
		return nil, errors.New("no upstream URL: set at least LLAMA_CPP_BASE_URL or OLLAMA_BASE_URL in deploy/.env")
	}
	c.Llama.BaseURL = c.BaseURLForBackend(c.Llama.Backend)
	if c.Llama.BaseURL == "" {
		if c.Llama.Backend == "ollama" {
			return nil, fmt.Errorf("active backend is ollama but OLLAMA_BASE_URL is missing or unresolved in deploy/.env")
		}
		return nil, fmt.Errorf("active backend is llamacpp but LLAMA_CPP_BASE_URL is missing or unresolved in deploy/.env")
	}
	if strings.Contains(c.Llama.UpstreamAPIKey, "${") {
		c.Llama.UpstreamAPIKey = ""
	}
	c.Llama.UpstreamAPIKey = strings.TrimSpace(c.Llama.UpstreamAPIKey)
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
	applyAuthEnv(&c)
	if err := adminuser.NormalizeWebLogin(&c.Auth.AdminUsername, &c.Auth.AdminPassword, &c.Auth.AdminPasswordHash); err != nil {
		return nil, fmt.Errorf("auth web login: %w", err)
	}
	return &c, nil
}

// applyAuthEnv 直接从进程环境读取 Web 登录项，避免 YAML 展开或 compose 未注入 ${} 时为空。
func applyAuthEnv(c *Config) {
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ADMIN_USERNAME")); v != "" && !strings.Contains(v, "${") {
		c.Auth.AdminUsername = v
	}
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ADMIN_PASSWORD_HASH")); looksLikePasswordHash(v) {
		c.Auth.AdminPasswordHash = v
	}
	if v := os.Getenv("LLM_GATEWAY_ADMIN_PASSWORD"); v != "" && !strings.Contains(v, "${") {
		c.Auth.AdminPassword = v
	}
}

func looksLikePasswordHash(v string) bool {
	return strings.HasPrefix(v, "pbkdf2$") && strings.Count(v, "$") >= 3 && len(v) > 40
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
