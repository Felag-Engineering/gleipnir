// Package config loads runtime configuration from environment variables.
// It is a leaf package with no imports outside the standard library.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"time"
)

// TimestampFormat is the canonical format for all timestamps produced by
// Gleipnir at runtime (audit steps, server records, etc.).
const TimestampFormat = time.RFC3339Nano

// DefaultPerCallMaxTokens is the per-API-call token limit used when the
// policy's max_tokens_per_run budget has not yet been reached.
const DefaultPerCallMaxTokens = 8192

// Config holds all runtime configuration for the Gleipnir server.
type Config struct {
	DBPath               string
	ListenAddr           string
	LogLevel             slog.Level
	MCPTimeout           time.Duration
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	IdleTimeout          time.Duration
	ApprovalScanInterval time.Duration
	DefaultProvider      string
	DefaultModel         string
}

// Load reads configuration from environment variables and applies defaults
// for any values not set or invalid.
func Load() Config {
	return Config{
		DBPath:               envOrDefault("GLEIPNIR_DB_PATH", "/data/gleipnir.db"),
		ListenAddr:           envOrDefault("GLEIPNIR_LISTEN_ADDR", ":8080"),
		LogLevel:             envLogLevel("GLEIPNIR_LOG_LEVEL", slog.LevelInfo),
		MCPTimeout:           envDuration("GLEIPNIR_MCP_TIMEOUT", 30*time.Second),
		ReadTimeout:          envDuration("GLEIPNIR_HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:         envDuration("GLEIPNIR_HTTP_WRITE_TIMEOUT", 15*time.Second),
		IdleTimeout:          envDuration("GLEIPNIR_HTTP_IDLE_TIMEOUT", 60*time.Second),
		ApprovalScanInterval: envDuration("GLEIPNIR_APPROVAL_SCAN_INTERVAL", 30*time.Second),
		DefaultProvider:      envOrDefault("GLEIPNIR_DEFAULT_PROVIDER", "anthropic"),
		DefaultModel:         envOrDefault("GLEIPNIR_DEFAULT_MODEL", "claude-sonnet-4-20250514"),
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: invalid value %q for %s, using default %s\n", v, key, def)
		return def
	}
	return d
}

func envLogLevel(key string, def slog.Level) slog.Level {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(v)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: invalid log level %q for %s, using default %s\n", v, key, def)
		return def
	}
	return level
}
