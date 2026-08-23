// Package config loads the runtime configuration from the environment.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
	"github.com/vance1852/drviercar/internal/logging"
)

// Config is the resolved server configuration.
type Config struct {
	Addr             string
	DatabasePath     string
	LogLevel         logging.Level
	SessionTTL       time.Duration
	RequestTimeout   time.Duration
	ShutdownTimeout  time.Duration
	WorkerInterval   time.Duration
	WorkerBatchSize  int
	WorkerBaseBackoff time.Duration
	MaxOpenConns     int
	BootstrapAdmin   string
	BootstrapSecret  string
}

// Load resolves the configuration from environment variables, applying the
// documented defaults. Secrets are never logged or echoed back.
func Load() (Config, error) {
	config := Config{
		Addr:              envString("DRVIERCAR_ADDR", "127.0.0.1:8080"),
		DatabasePath:      envString("DRVIERCAR_DB_PATH", "data/drviercar.sqlite"),
		LogLevel:          logging.Level(strings.ToLower(envString("DRVIERCAR_LOG_LEVEL", "info"))),
		BootstrapAdmin:    envString("DRVIERCAR_BOOTSTRAP_ADMIN", ""),
		BootstrapSecret:   os.Getenv("DRVIERCAR_BOOTSTRAP_SECRET"),
	}
	var err error
	if config.SessionTTL, err = envDuration("DRVIERCAR_SESSION_TTL", 8*time.Hour); err != nil {
		return Config{}, err
	}
	if config.RequestTimeout, err = envDuration("DRVIERCAR_REQUEST_TIMEOUT", 15*time.Second); err != nil {
		return Config{}, err
	}
	if config.ShutdownTimeout, err = envDuration("DRVIERCAR_SHUTDOWN_TIMEOUT", 10*time.Second); err != nil {
		return Config{}, err
	}
	if config.WorkerInterval, err = envDuration("DRVIERCAR_WORKER_INTERVAL", time.Second); err != nil {
		return Config{}, err
	}
	if config.WorkerBaseBackoff, err = envDuration("DRVIERCAR_WORKER_BACKOFF", 2*time.Second); err != nil {
		return Config{}, err
	}
	if config.WorkerBatchSize, err = envInt("DRVIERCAR_WORKER_BATCH", 5); err != nil {
		return Config{}, err
	}
	if config.MaxOpenConns, err = envInt("DRVIERCAR_DB_MAX_CONNS", 4); err != nil {
		return Config{}, err
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

// Validate checks the resolved values.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Addr) == "" {
		return apperr.Invalidf("config_addr_required", "监听地址不能为空")
	}
	if strings.TrimSpace(c.DatabasePath) == "" {
		return apperr.Invalidf("config_database_required", "数据库路径不能为空")
	}
	switch c.LogLevel {
	case logging.LevelDebug, logging.LevelInfo, logging.LevelWarn, logging.LevelError:
	default:
		return apperr.Invalidf("config_log_level_invalid", "未知的日志级别 %q", string(c.LogLevel))
	}
	if c.SessionTTL < time.Minute {
		return apperr.Invalidf("config_session_ttl_too_short", "会话有效期至少 1 分钟")
	}
	if c.WorkerBatchSize <= 0 {
		return apperr.Invalidf("config_worker_batch_invalid", "后台任务批量必须大于 0")
	}
	if c.MaxOpenConns <= 0 {
		return apperr.Invalidf("config_db_conns_invalid", "数据库连接数必须大于 0")
	}
	if c.BootstrapAdmin != "" && len(c.BootstrapSecret) < 8 {
		return apperr.Invalidf("config_bootstrap_secret_weak",
			"初始化管理员口令长度至少 8 位")
	}
	return nil
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, apperr.Invalidf("config_int_invalid", "%s 必须是整数", key)
	}
	return value, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, apperr.Invalidf("config_duration_invalid", "%s 必须是合法的时间长度", key)
	}
	return value, nil
}
