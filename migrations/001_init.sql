CREATE TABLE IF NOT EXISTS api_keys (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  name VARCHAR(128) NOT NULL,
  prefix VARCHAR(32) NOT NULL,
  key_hash CHAR(64) NOT NULL UNIQUE,
  status VARCHAR(16) NOT NULL DEFAULT 'active',
  expires_at DATETIME(6) NULL,
  rpm INT NOT NULL DEFAULT 60,
  allowed_models TEXT NULL,
  allowed_tools TEXT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  last_used_at DATETIME(6) NULL,
  UNIQUE KEY uk_key_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS models (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  model_key VARCHAR(128) NOT NULL UNIQUE,
  display_name VARCHAR(255) NOT NULL,
  source_type VARCHAR(32) NOT NULL,
  hf_repo VARCHAR(512) NULL,
  hf_revision VARCHAR(128) NULL,
  filename VARCHAR(512) NOT NULL,
  local_path VARCHAR(1024) NOT NULL,
  quantization VARCHAR(64) NULL,
  llama_model_name VARCHAR(1024) NULL,
  size_bytes BIGINT UNSIGNED NULL,
  sha256 CHAR(64) NULL,
  status VARCHAR(32) NOT NULL DEFAULT 'registered',
  enabled TINYINT(1) NOT NULL DEFAULT 1,
  runtime_json LONGTEXT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  last_loaded_at DATETIME(6) NULL,
  last_used_at DATETIME(6) NULL,
  INDEX idx_models_status (status), INDEX idx_models_enabled (enabled)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS request_logs (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  request_id VARCHAR(64) NOT NULL,
  api_key_id BIGINT UNSIGNED NULL,
  api_key_name VARCHAR(128) NULL,
  client_ip VARCHAR(64) NULL,
  client_name VARCHAR(128) NULL,
  user_agent VARCHAR(512) NULL,
  endpoint VARCHAR(128) NOT NULL,
  request_url VARCHAR(2048) NULL,
  provider_id VARCHAR(64) NULL,
  method VARCHAR(16) NOT NULL,
  model_requested VARCHAR(255) NULL,
  model_actual VARCHAR(1024) NULL,
  stream TINYINT(1) NOT NULL DEFAULT 0,
  status VARCHAR(32) NOT NULL DEFAULT 'running',
  http_status SMALLINT UNSIGNED NULL,
  upstream_http_status SMALLINT UNSIGNED NULL,
  started_at DATETIME(6) NOT NULL,
  finished_at DATETIME(6) NULL,
  latency_ms BIGINT UNSIGNED NULL,
  ttft_ms BIGINT UNSIGNED NULL,
  input_tokens BIGINT UNSIGNED NULL,
  output_tokens BIGINT UNSIGNED NULL,
  total_tokens BIGINT UNSIGNED NULL,
  token_source VARCHAR(32) NULL,
  request_bytes BIGINT UNSIGNED NULL,
  response_bytes BIGINT UNSIGNED NULL,
  error_code VARCHAR(64) NULL,
  error_message TEXT NULL,
  analysis_json LONGTEXT NULL,
  INDEX idx_req_id (request_id), INDEX idx_started (started_at), INDEX idx_api_key (api_key_id), INDEX idx_model (model_actual), INDEX idx_provider_started (provider_id, started_at), INDEX idx_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS request_payloads (
  request_id VARCHAR(64) NOT NULL PRIMARY KEY,
  request_body LONGTEXT NULL,
  response_body LONGTEXT NULL,
  request_headers_json LONGTEXT NULL,
  upstream_headers_json LONGTEXT NULL,
  created_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS request_stream_events (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  request_id VARCHAR(64) NOT NULL,
  seq INT NOT NULL,
  at_ms BIGINT UNSIGNED NOT NULL,
  kind VARCHAR(32) NOT NULL,
  field_name VARCHAR(32) NULL,
  delta_len INT UNSIGNED NULL,
  preview VARCHAR(512) NULL,
  raw_line TEXT NULL,
  INDEX idx_req_stream (request_id, seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS benchmark_tasks (
  task_id VARCHAR(64) NOT NULL PRIMARY KEY,
  api_key_id BIGINT UNSIGNED NULL,
  scenario VARCHAR(32) NOT NULL DEFAULT 'performance',
  status VARCHAR(16) NOT NULL DEFAULT 'running',
  provider_id VARCHAR(64) NULL,
  backend VARCHAR(32) NULL,
  model_key VARCHAR(128) NULL,
  model_id VARCHAR(1024) NULL,
  prompt_sha256 CHAR(64) NULL,
  prompt_length BIGINT UNSIGNED NOT NULL DEFAULT 0,
  max_tokens INT UNSIGNED NOT NULL DEFAULT 0,
  config_json LONGTEXT NULL,
  run_count INT UNSIGNED NOT NULL DEFAULT 0,
  started_at DATETIME(6) NOT NULL,
  finished_at DATETIME(6) NULL,
  latency_ms BIGINT UNSIGNED NULL,
  error_code VARCHAR(64) NULL,
  error_message VARCHAR(512) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  INDEX idx_benchmark_tasks_status_started (status, started_at),
  INDEX idx_benchmark_tasks_key_started (api_key_id, started_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS benchmark_runs (
  run_id VARCHAR(64) NOT NULL PRIMARY KEY,
  task_id VARCHAR(64) NOT NULL,
  run_no INT UNSIGNED NOT NULL,
  request_id VARCHAR(64) NOT NULL,
  status VARCHAR(16) NOT NULL DEFAULT 'running',
  provider_id VARCHAR(64) NULL,
  model_key VARCHAR(128) NULL,
  model_id VARCHAR(1024) NULL,
  started_at DATETIME(6) NOT NULL,
  finished_at DATETIME(6) NULL,
  latency_ms BIGINT UNSIGNED NULL,
  model_load_ms BIGINT UNSIGNED NULL,
  model_load_status VARCHAR(24) NOT NULL DEFAULT 'unknown',
  inference_latency_ms BIGINT UNSIGNED NULL,
  inference_latency_status VARCHAR(24) NOT NULL DEFAULT 'unknown',
  ttft_status VARCHAR(24) NOT NULL DEFAULT 'unknown',
  model_round_durations_json LONGTEXT NULL,
  http_status SMALLINT UNSIGNED NULL,
  response_bytes BIGINT UNSIGNED NULL,
  input_tokens BIGINT UNSIGNED NULL,
  output_tokens BIGINT UNSIGNED NULL,
  total_tokens BIGINT UNSIGNED NULL,
  ttft_ms BIGINT UNSIGNED NULL,
  stream_event_count INT UNSIGNED NOT NULL DEFAULT 0,
  reasoning_chars INT UNSIGNED NOT NULL DEFAULT 0,
  content_chars INT UNSIGNED NOT NULL DEFAULT 0,
  error_code VARCHAR(64) NULL,
  error_message VARCHAR(512) NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  UNIQUE KEY uk_benchmark_run_task_no (task_id, run_no),
  UNIQUE KEY uk_benchmark_run_request (request_id),
  INDEX idx_benchmark_runs_task_status (task_id, status),
  CONSTRAINT fk_benchmark_runs_task FOREIGN KEY (task_id)
    REFERENCES benchmark_tasks(task_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS gateway_settings (
  key_name VARCHAR(64) NOT NULL PRIMARY KEY,
  value_json LONGTEXT NOT NULL,
  updated_at DATETIME(6) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Provider 模型价格采用追加版本，不覆盖历史价格；单价单位为 USD / 1,000,000 tokens。
CREATE TABLE IF NOT EXISTS model_prices (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  provider_id VARCHAR(64) NOT NULL,
  model_id VARCHAR(255) NOT NULL,
  input_price_per_million DECIMAL(24,12) NOT NULL,
  output_price_per_million DECIMAL(24,12) NOT NULL,
  currency CHAR(3) NOT NULL DEFAULT 'USD',
  effective_at DATETIME(6) NOT NULL,
  created_at DATETIME(6) NOT NULL,
  UNIQUE KEY uk_model_price_effective (provider_id, model_id, effective_at), INDEX idx_model_price_lookup (provider_id, model_id, effective_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS request_tool_call_events (
  request_id VARCHAR(64) NOT NULL,
  seq SMALLINT UNSIGNED NOT NULL,
  round_no TINYINT UNSIGNED NOT NULL,
  tool_name VARCHAR(64) NOT NULL DEFAULT '',
  status VARCHAR(16) NOT NULL,
  duration_ms BIGINT UNSIGNED NOT NULL DEFAULT 0,
  result_bytes BIGINT UNSIGNED NOT NULL DEFAULT 0,
  error_code VARCHAR(64) NULL,
  created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  PRIMARY KEY (request_id, seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
