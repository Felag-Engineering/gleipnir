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
	// Swap slog default with a buffer-backed handler to capture log output.
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	original := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(original) })

	l := NewLoader()
	err := l.Init(context.Background(), config.Config{PluginsEnabled: true})
	if err != nil {
		t.Fatalf("Init() with PluginsEnabled=true: got error %v, want nil", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, "plugin loader enabled") {
		t.Errorf("expected log to contain %q, got: %s", "plugin loader enabled", logged)
	}
}
