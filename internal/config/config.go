package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all application configuration.
type Config struct {
	Server    ServerConfig
	Engine    EngineConfig
	Auth      AuthConfig
	RateLimit RateLimitConfig
	Logging   LoggingConfig
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Host            string
	Port            int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
}

// EngineConfig holds job processing engine settings.
type EngineConfig struct {
	MaxWorkers     int    // Max concurrent workers
	QueueSize      int    // Max pending jobs in queue
	DefaultRetries int    // Default max retries per job
	DataDir        string // Directory for temporary/intermediate data
	RAMCapMB       int64  // Maximum RAM budget in MB (default: 500)
	QueueRAMCapMB  int64  // RAM budget for priority queues in MB (default: 400)
}

// AuthConfig holds API authentication settings.
type AuthConfig struct {
	Enabled bool              // Enable API key authentication
	APIKeys map[string]string // api_key → client_id mapping
}

// RateLimitConfig holds rate limiting settings.
type RateLimitConfig struct {
	Enabled        bool // Enable HTTP rate limiting
	RequestsPerMin int  // Max requests per client per minute
	BurstSize      int  // Burst allowance above steady rate
}

// LoggingConfig holds logging settings.
type LoggingConfig struct {
	Level  string // "debug" | "info" | "warn" | "error"
	Format string // "json" | "text"
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		Server: ServerConfig{
			Host:            envStr("SERVER_HOST", "0.0.0.0"),
			Port:            envInt("SERVER_PORT", 8080),
			ReadTimeout:     envDuration("SERVER_READ_TIMEOUT", 30*time.Second),
			WriteTimeout:    envDuration("SERVER_WRITE_TIMEOUT", 60*time.Second),
			IdleTimeout:     envDuration("SERVER_IDLE_TIMEOUT", 120*time.Second),
			ShutdownTimeout: envDuration("SERVER_SHUTDOWN_TIMEOUT", 15*time.Second),
		},
		Engine: EngineConfig{
			MaxWorkers:     envInt("ENGINE_MAX_WORKERS", 16),
			QueueSize:      envInt("ENGINE_QUEUE_SIZE", 10000),
			DefaultRetries: envInt("ENGINE_DEFAULT_RETRIES", 3),
			DataDir:        envStr("ENGINE_DATA_DIR", "./data"),
			RAMCapMB:       int64(envInt("ENGINE_RAM_CAP_MB", 500)),
			QueueRAMCapMB:  int64(envInt("ENGINE_QUEUE_RAM_CAP_MB", 400)),
		},
		Auth: AuthConfig{
			Enabled: envStr("AUTH_ENABLED", "false") == "true",
			APIKeys: parseAPIKeys(envStr("AUTH_API_KEYS", "")),
		},
		RateLimit: RateLimitConfig{
			Enabled:        envStr("RATE_LIMIT_ENABLED", "true") == "true",
			RequestsPerMin: envInt("RATE_LIMIT_RPM", 120),
			BurstSize:      envInt("RATE_LIMIT_BURST", 30),
		},
		Logging: LoggingConfig{
			Level:  envStr("LOG_LEVEL", "info"),
			Format: envStr("LOG_FORMAT", "json"),
		},
	}
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

// parseAPIKeys parses comma-separated key:client_id pairs.
// Format: "key1:client1,key2:client2"
func parseAPIKeys(raw string) map[string]string {
	keys := make(map[string]string)
	if raw == "" {
		return keys
	}
	for _, pair := range splitTrim(raw, ",") {
		parts := splitTrim(pair, ":")
		if len(parts) == 2 {
			keys[parts[0]] = parts[1]
		}
	}
	return keys
}

func splitTrim(s, sep string) []string {
	parts := make([]string, 0)
	for _, p := range strings.Split(s, sep) {
		t := strings.TrimSpace(p)
		if t != "" {
			parts = append(parts, t)
		}
	}
	return parts
}
