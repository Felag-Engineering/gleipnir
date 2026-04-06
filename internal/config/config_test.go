package config

import (
	"log/slog"
	"testing"
	"time"
)

func TestLoad_Defaults(t *testing.T) {
	// Unset all env vars so we get pure defaults.
	for _, key := range []string{
		"GLEIPNIR_DB_PATH",
		"GLEIPNIR_LISTEN_ADDR",
		"GLEIPNIR_LOG_LEVEL",
		"GLEIPNIR_MCP_TIMEOUT",
		"GLEIPNIR_HTTP_READ_TIMEOUT",
		"GLEIPNIR_HTTP_WRITE_TIMEOUT",
		"GLEIPNIR_HTTP_IDLE_TIMEOUT",
		"GLEIPNIR_APPROVAL_SCAN_INTERVAL",
		"GLEIPNIR_DEFAULT_FEEDBACK_TIMEOUT",
		"GLEIPNIR_FEEDBACK_SCAN_INTERVAL",
		"GLEIPNIR_ENCRYPTION_KEY",
	} {
		t.Setenv(key, "")
	}

	cfg := Load()

	if cfg.DBPath != "/data/gleipnir.db" {
		t.Errorf("DBPath: got %q, want /data/gleipnir.db", cfg.DBPath)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr: got %q, want :8080", cfg.ListenAddr)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel: got %v, want INFO", cfg.LogLevel)
	}
	if cfg.MCPTimeout != 30*time.Second {
		t.Errorf("MCPTimeout: got %v, want 30s", cfg.MCPTimeout)
	}
	if cfg.ReadTimeout != 15*time.Second {
		t.Errorf("ReadTimeout: got %v, want 15s", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout != 15*time.Second {
		t.Errorf("WriteTimeout: got %v, want 15s", cfg.WriteTimeout)
	}
	if cfg.IdleTimeout != 60*time.Second {
		t.Errorf("IdleTimeout: got %v, want 60s", cfg.IdleTimeout)
	}
	if cfg.ApprovalScanInterval != 30*time.Second {
		t.Errorf("ApprovalScanInterval: got %v, want 30s", cfg.ApprovalScanInterval)
	}
	if cfg.DefaultFeedbackTimeout != 30*time.Minute {
		t.Errorf("DefaultFeedbackTimeout: got %v, want 30m", cfg.DefaultFeedbackTimeout)
	}
	if cfg.FeedbackScanInterval != 30*time.Second {
		t.Errorf("FeedbackScanInterval: got %v, want 30s", cfg.FeedbackScanInterval)
	}
	if cfg.EncryptionKey != "" {
		t.Errorf("EncryptionKey: got %q, want empty", cfg.EncryptionKey)
	}
}

func TestLoad_Overrides(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		check func(t *testing.T, cfg Config)
	}{
		{
			name: "db path",
			env:  map[string]string{"GLEIPNIR_DB_PATH": "/tmp/test.db"},
			check: func(t *testing.T, cfg Config) {
				if cfg.DBPath != "/tmp/test.db" {
					t.Errorf("got %q, want /tmp/test.db", cfg.DBPath)
				}
			},
		},
		{
			name: "listen addr",
			env:  map[string]string{"GLEIPNIR_LISTEN_ADDR": ":9090"},
			check: func(t *testing.T, cfg Config) {
				if cfg.ListenAddr != ":9090" {
					t.Errorf("got %q, want :9090", cfg.ListenAddr)
				}
			},
		},
		{
			name: "mcp timeout",
			env:  map[string]string{"GLEIPNIR_MCP_TIMEOUT": "10s"},
			check: func(t *testing.T, cfg Config) {
				if cfg.MCPTimeout != 10*time.Second {
					t.Errorf("got %v, want 10s", cfg.MCPTimeout)
				}
			},
		},
		{
			name: "http read timeout",
			env:  map[string]string{"GLEIPNIR_HTTP_READ_TIMEOUT": "5s"},
			check: func(t *testing.T, cfg Config) {
				if cfg.ReadTimeout != 5*time.Second {
					t.Errorf("got %v, want 5s", cfg.ReadTimeout)
				}
			},
		},
		{
			name: "http write timeout",
			env:  map[string]string{"GLEIPNIR_HTTP_WRITE_TIMEOUT": "20s"},
			check: func(t *testing.T, cfg Config) {
				if cfg.WriteTimeout != 20*time.Second {
					t.Errorf("got %v, want 20s", cfg.WriteTimeout)
				}
			},
		},
		{
			name: "http idle timeout",
			env:  map[string]string{"GLEIPNIR_HTTP_IDLE_TIMEOUT": "120s"},
			check: func(t *testing.T, cfg Config) {
				if cfg.IdleTimeout != 120*time.Second {
					t.Errorf("got %v, want 120s", cfg.IdleTimeout)
				}
			},
		},
		{
			name: "approval scan interval",
			env:  map[string]string{"GLEIPNIR_APPROVAL_SCAN_INTERVAL": "1m"},
			check: func(t *testing.T, cfg Config) {
				if cfg.ApprovalScanInterval != time.Minute {
					t.Errorf("got %v, want 1m", cfg.ApprovalScanInterval)
				}
			},
		},
		{
			name: "default feedback timeout",
			env:  map[string]string{"GLEIPNIR_DEFAULT_FEEDBACK_TIMEOUT": "1h"},
			check: func(t *testing.T, cfg Config) {
				if cfg.DefaultFeedbackTimeout != time.Hour {
					t.Errorf("got %v, want 1h", cfg.DefaultFeedbackTimeout)
				}
			},
		},
		{
			name: "feedback scan interval",
			env:  map[string]string{"GLEIPNIR_FEEDBACK_SCAN_INTERVAL": "1m"},
			check: func(t *testing.T, cfg Config) {
				if cfg.FeedbackScanInterval != time.Minute {
					t.Errorf("got %v, want 1m", cfg.FeedbackScanInterval)
				}
			},
		},
		{
			name: "encryption key",
			env:  map[string]string{"GLEIPNIR_ENCRYPTION_KEY": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
			check: func(t *testing.T, cfg Config) {
				if cfg.EncryptionKey != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
					t.Errorf("got %q, want hex key", cfg.EncryptionKey)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Clear all env vars first.
			for _, key := range []string{
				"GLEIPNIR_DB_PATH", "GLEIPNIR_LISTEN_ADDR", "GLEIPNIR_LOG_LEVEL",
				"GLEIPNIR_MCP_TIMEOUT", "GLEIPNIR_HTTP_READ_TIMEOUT",
				"GLEIPNIR_HTTP_WRITE_TIMEOUT", "GLEIPNIR_HTTP_IDLE_TIMEOUT",
				"GLEIPNIR_APPROVAL_SCAN_INTERVAL",
				"GLEIPNIR_DEFAULT_FEEDBACK_TIMEOUT", "GLEIPNIR_FEEDBACK_SCAN_INTERVAL",
				"GLEIPNIR_ENCRYPTION_KEY",
			} {
				t.Setenv(key, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			cfg := Load()
			tc.check(t, cfg)
		})
	}
}

func TestLoad_LogLevel(t *testing.T) {
	tests := []struct {
		name      string
		envValue  string
		wantLevel slog.Level
	}{
		{"debug lowercase", "debug", slog.LevelDebug},
		{"info lowercase", "info", slog.LevelInfo},
		{"warn lowercase", "warn", slog.LevelWarn},
		{"error lowercase", "error", slog.LevelError},
		{"DEBUG uppercase", "DEBUG", slog.LevelDebug},
		{"INFO uppercase", "INFO", slog.LevelInfo},
		{"WARN uppercase", "WARN", slog.LevelWarn},
		{"ERROR uppercase", "ERROR", slog.LevelError},
		{"invalid falls back to info", "bogus", slog.LevelInfo},
		{"empty falls back to info", "", slog.LevelInfo},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GLEIPNIR_LOG_LEVEL", tc.envValue)
			cfg := Load()
			if cfg.LogLevel != tc.wantLevel {
				t.Errorf("LogLevel: got %v, want %v", cfg.LogLevel, tc.wantLevel)
			}
		})
	}
}

func TestLoad_InvalidDuration(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want time.Duration
	}{
		{"invalid mcp timeout falls back", "GLEIPNIR_MCP_TIMEOUT", 30 * time.Second},
		{"invalid read timeout falls back", "GLEIPNIR_HTTP_READ_TIMEOUT", 15 * time.Second},
		{"invalid write timeout falls back", "GLEIPNIR_HTTP_WRITE_TIMEOUT", 15 * time.Second},
		{"invalid idle timeout falls back", "GLEIPNIR_HTTP_IDLE_TIMEOUT", 60 * time.Second},
		{"invalid approval scan interval falls back", "GLEIPNIR_APPROVAL_SCAN_INTERVAL", 30 * time.Second},
		{"invalid default feedback timeout falls back", "GLEIPNIR_DEFAULT_FEEDBACK_TIMEOUT", 30 * time.Minute},
		{"invalid feedback scan interval falls back", "GLEIPNIR_FEEDBACK_SCAN_INTERVAL", 30 * time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, "not-a-duration")
			cfg := Load()
			var got time.Duration
			switch tc.key {
			case "GLEIPNIR_MCP_TIMEOUT":
				got = cfg.MCPTimeout
			case "GLEIPNIR_HTTP_READ_TIMEOUT":
				got = cfg.ReadTimeout
			case "GLEIPNIR_HTTP_WRITE_TIMEOUT":
				got = cfg.WriteTimeout
			case "GLEIPNIR_HTTP_IDLE_TIMEOUT":
				got = cfg.IdleTimeout
			case "GLEIPNIR_APPROVAL_SCAN_INTERVAL":
				got = cfg.ApprovalScanInterval
			case "GLEIPNIR_DEFAULT_FEEDBACK_TIMEOUT":
				got = cfg.DefaultFeedbackTimeout
			case "GLEIPNIR_FEEDBACK_SCAN_INTERVAL":
				got = cfg.FeedbackScanInterval
			}
			if got != tc.want {
				t.Errorf("%s: got %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}
