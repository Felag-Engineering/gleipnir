package logctx_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/rapp992/gleipnir/internal/logctx"
)

func TestWithRunCorrelation_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, nil)
	slog.SetDefault(slog.New(handler))

	ctx := logctx.WithRunCorrelation(context.Background(), "run-123", "pol-456")
	logger := logctx.Logger(ctx)
	logger.InfoContext(ctx, "test message")

	output := buf.String()
	if output == "" {
		t.Fatal("expected log output, got empty string")
	}

	// Verify both correlation IDs appear in the structured output.
	if !bytes.Contains(buf.Bytes(), []byte(`"run_id":"run-123"`)) {
		t.Errorf("expected run_id in log output, got: %s", output)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"policy_id":"pol-456"`)) {
		t.Errorf("expected policy_id in log output, got: %s", output)
	}
}

func TestLogger_BareContext_ReturnsDefault(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, nil)
	slog.SetDefault(slog.New(handler))

	// Logger on a context without correlation should return the default logger
	// and not include run_id or policy_id.
	logger := logctx.Logger(context.Background())
	logger.InfoContext(context.Background(), "bare context")

	output := buf.String()
	if output == "" {
		t.Fatal("expected log output, got empty string")
	}
	if bytes.Contains(buf.Bytes(), []byte("run_id")) {
		t.Errorf("expected no run_id in log output for bare context, got: %s", output)
	}
	if bytes.Contains(buf.Bytes(), []byte("policy_id")) {
		t.Errorf("expected no policy_id in log output for bare context, got: %s", output)
	}
}
