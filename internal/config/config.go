// Package config loads runtime configuration from environment variables.
// It is a leaf package with no imports outside the standard library.
package config

import (
	"encoding/hex"
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
	DBPath                 string
	ListenAddr             string
	LogLevel               slog.Level
	MCPTimeout             time.Duration
	ReadTimeout            time.Duration
	WriteTimeout           time.Duration
	IdleTimeout            time.Duration
	ApprovalScanInterval   time.Duration
	DefaultFeedbackTimeout time.Duration
	FeedbackScanInterval   time.Duration
	EncryptionKey          string
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
		DBPath:                 envOrDefault("GLEIPNIR_DB_PATH", "/data/gleipnir.db"),
		ListenAddr:             envOrDefault("GLEIPNIR_LISTEN_ADDR", ":8080"),
		LogLevel:               envLogLevel("GLEIPNIR_LOG_LEVEL", slog.LevelInfo),
		MCPTimeout:             envDuration("GLEIPNIR_MCP_TIMEOUT", 30*time.Second),
		ReadTimeout:            envDuration("GLEIPNIR_HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:           envDuration("GLEIPNIR_HTTP_WRITE_TIMEOUT", 15*time.Second),
		IdleTimeout:            envDuration("GLEIPNIR_HTTP_IDLE_TIMEOUT", 60*time.Second),
		ApprovalScanInterval:   envDuration("GLEIPNIR_APPROVAL_SCAN_INTERVAL", 30*time.Second),
		DefaultFeedbackTimeout: envDuration("GLEIPNIR_DEFAULT_FEEDBACK_TIMEOUT", 30*time.Minute),
		FeedbackScanInterval:   envDuration("GLEIPNIR_FEEDBACK_SCAN_INTERVAL", 30*time.Second),
		EncryptionKey:          raw,
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
