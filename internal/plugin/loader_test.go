package plugin

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/infra/config"
)

func TestLoader_Init_Disabled(t *testing.T) {
	l := NewLoader()
	err := l.Init(context.Background(), config.Config{PluginsEnabled: false})
	if err != nil {
		t.Fatalf("Init() with PluginsEnabled=false: got error %v, want nil", err)
	}
}

func TestLoader_Init_Enabled(t *testing.T) {
	logged := captureLogs(t)

	l := NewLoader()
	err := l.Init(context.Background(), config.Config{PluginsEnabled: true})
	if err != nil {
		t.Fatalf("Init() with PluginsEnabled=true: got error %v, want nil", err)
	}

	if !strings.Contains(logged.String(), "plugin loader enabled") {
		t.Errorf("expected log to contain %q, got: %s", "plugin loader enabled", logged.String())
	}
	if l.Verifier() == nil {
		t.Errorf("Verifier() = nil; want non-nil when plugins enabled")
	}
	if l.Verifier().AllowUnsigned {
		t.Errorf("AllowUnsigned: got true, want false (default)")
	}
}

func TestLoader_Init_PermissiveMode(t *testing.T) {
	logged := captureLogs(t)

	l := NewLoader()
	err := l.Init(context.Background(), config.Config{
		PluginsEnabled:       true,
		AllowUnsignedPlugins: true,
	})
	if err != nil {
		t.Fatalf("Init() with permissive mode: %v", err)
	}

	if l.Verifier() == nil || !l.Verifier().AllowUnsigned {
		t.Fatalf("Verifier(): got %+v, want AllowUnsigned=true", l.Verifier())
	}
	out := logged.String()
	// The permissive-mode banner is the operator's last warning before
	// audit-only signals take over. Hard-pin its presence.
	if !strings.Contains(out, "GLEIPNIR_ALLOW_UNSIGNED_PLUGINS=true") {
		t.Errorf("permissive-mode log missing env-var name; got: %s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("permissive-mode log not at WARN level; got: %s", out)
	}
}

func TestLoader_Init_Disabled_LeavesVerifierNil(t *testing.T) {
	l := NewLoader()
	if err := l.Init(context.Background(), config.Config{PluginsEnabled: false}); err != nil {
		t.Fatalf("Init disabled: %v", err)
	}
	if l.Verifier() != nil {
		t.Errorf("Verifier() = %+v; want nil when plugins disabled", l.Verifier())
	}
}

// TestLoader_StartWatcher_DisabledIsNoOp asserts that StartWatcher is a no-op
// when GLEIPNIR_PLUGINS_ENABLED=false (i.e. Init was never called, verifier is nil).
func TestLoader_StartWatcher_DisabledIsNoOp(t *testing.T) {
	l := NewLoader()
	if err := l.Init(context.Background(), config.Config{PluginsEnabled: false}); err != nil {
		t.Fatalf("Init disabled: %v", err)
	}
	// StartWatcher must return without panicking or starting anything.
	// Pass nil for q — the code must not reach any q calls when disabled.
	l.StartWatcher(context.Background(), nil, t.TempDir())
}

// captureLogs swaps slog.Default with a buffer-backed handler for the
// duration of the test and returns the buffer.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	original := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(original) })
	return &buf
}
