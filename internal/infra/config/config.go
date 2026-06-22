// Package config loads runtime configuration from environment variables.
// It is a leaf package with no imports outside the standard library.
package config

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strconv"
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
	DBPath                    string
	ListenAddr                string
	LogLevel                  slog.Level
	MCPTimeout                time.Duration
	ReadTimeout               time.Duration
	WriteTimeout              time.Duration
	IdleTimeout               time.Duration
	ApprovalScanInterval      time.Duration
	DefaultFeedbackTimeout    time.Duration
	FeedbackScanInterval      time.Duration
	PluginRequestScanInterval time.Duration
	DrainTimeout              time.Duration
	PIDFile                   string
	EncryptionKey             string
	AllowUnsignedPlugins      bool
	PluginsDir                string
	OAuthRefreshInterval      time.Duration // GLEIPNIR_OAUTH_REFRESH_INTERVAL, default 5m
	OAuthRefreshLead          time.Duration // GLEIPNIR_OAUTH_REFRESH_LEAD, default 15m
	PluginDedupSweepInterval  time.Duration // GLEIPNIR_PLUGIN_DEDUP_SWEEP_INTERVAL, default 10m

	// LLM transient-failure retry. A 429 (TPM/RPM rate limit) or 5xx response
	// from a provider is retried up to LLMRetryMaxAttempts times, honoring the
	// provider's Retry-After header when present and otherwise backing off
	// exponentially from LLMRetryInitialBackoff up to LLMRetryMaxBackoff.
	LLMRetryMaxAttempts    int           // GLEIPNIR_LLM_RETRY_MAX_ATTEMPTS, default 4 (1 disables retry)
	LLMRetryInitialBackoff time.Duration // GLEIPNIR_LLM_RETRY_INITIAL_BACKOFF, default 1s
	LLMRetryMaxBackoff     time.Duration // GLEIPNIR_LLM_RETRY_MAX_BACKOFF, default 30s
}

// Load reads configuration from environment variables, applies defaults for
// any values not set or invalid, and validates required fields.
//
// It returns an error if GLEIPNIR_ENCRYPTION_KEY is missing or malformed.
// The server cannot start without a valid encryption key because provider API
// keys and webhook secrets are stored encrypted at rest using AES-256.
func Load() (Config, error) {
	raw := os.Getenv("GLEIPNIR_ENCRYPTION_KEY")
	if err := validateEncryptionKey(raw); err != nil {
		return Config{}, err
	}

	return Config{
		DBPath:                    envOrDefault("GLEIPNIR_DB_PATH", "/data/gleipnir.db"),
		ListenAddr:                envOrDefault("GLEIPNIR_LISTEN_ADDR", ":8080"),
		LogLevel:                  envLogLevel("GLEIPNIR_LOG_LEVEL", slog.LevelInfo),
		MCPTimeout:                envDuration("GLEIPNIR_MCP_TIMEOUT", 30*time.Second),
		ReadTimeout:               envDuration("GLEIPNIR_HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:              envDuration("GLEIPNIR_HTTP_WRITE_TIMEOUT", 15*time.Second),
		IdleTimeout:               envDuration("GLEIPNIR_HTTP_IDLE_TIMEOUT", 60*time.Second),
		ApprovalScanInterval:      envDuration("GLEIPNIR_APPROVAL_SCAN_INTERVAL", 30*time.Second),
		DefaultFeedbackTimeout:    envDuration("GLEIPNIR_DEFAULT_FEEDBACK_TIMEOUT", 30*time.Minute),
		FeedbackScanInterval:      envDuration("GLEIPNIR_FEEDBACK_SCAN_INTERVAL", 30*time.Second),
		PluginRequestScanInterval: envDuration("GLEIPNIR_PLUGIN_REQUEST_SCAN_INTERVAL", 30*time.Second),
		DrainTimeout:              envDuration("GLEIPNIR_DRAIN_TIMEOUT", 5*time.Minute),
		PIDFile:                   envOrDefault("GLEIPNIR_PID_FILE", "/var/run/gleipnir.pid"),
		EncryptionKey:             raw,
		AllowUnsignedPlugins:      envBool("GLEIPNIR_ALLOW_UNSIGNED_PLUGINS", false),
		PluginsDir:                envOrDefault("GLEIPNIR_PLUGINS_DIR", "/plugins"),
		OAuthRefreshInterval:      envDuration("GLEIPNIR_OAUTH_REFRESH_INTERVAL", 5*time.Minute),
		OAuthRefreshLead:          envDuration("GLEIPNIR_OAUTH_REFRESH_LEAD", 15*time.Minute),
		PluginDedupSweepInterval:  envDuration("GLEIPNIR_PLUGIN_DEDUP_SWEEP_INTERVAL", 10*time.Minute),
		LLMRetryMaxAttempts:       envInt("GLEIPNIR_LLM_RETRY_MAX_ATTEMPTS", 4),
		LLMRetryInitialBackoff:    envDuration("GLEIPNIR_LLM_RETRY_INITIAL_BACKOFF", 1*time.Second),
		LLMRetryMaxBackoff:        envDuration("GLEIPNIR_LLM_RETRY_MAX_BACKOFF", 30*time.Second),
	}, nil
}

// validateEncryptionKey checks that the value of GLEIPNIR_ENCRYPTION_KEY is a
// valid hex string that decodes to exactly 32 bytes (AES-256).
func validateEncryptionKey(raw string) error {
	if raw == "" {
		return fmt.Errorf(
			"GLEIPNIR_ENCRYPTION_KEY is required (64-char hex, 32-byte AES-256 key); " +
				"generate one with: openssl rand -hex 32",
		)
	}

	decoded, err := hex.DecodeString(raw)
	if err != nil {
		return fmt.Errorf(
			"GLEIPNIR_ENCRYPTION_KEY is not valid hex: %w; "+
				"generate a valid key with: openssl rand -hex 32",
			err,
		)
	}

	if len(decoded) != 32 {
		return fmt.Errorf(
			"GLEIPNIR_ENCRYPTION_KEY decoded to %d bytes, want 32 (AES-256 requires a 64-char hex string); "+
				"generate a valid key with: openssl rand -hex 32",
			len(decoded),
		)
	}

	return nil
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

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: invalid int value %q for %s, using default %d\n", v, key, def)
		return def
	}
	return n
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: invalid bool value %q for %s, using default %v\n", v, key, def)
		return def
	}
	return b
}
