package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestClassifyPluginError verifies that each plugin dispatch sentinel maps to a
// distinct, agent-facing message, and that wrapped errors (the main.go
// ConnFactory wraps these with the instance name via fmt.Errorf("%w: %q", ...))
// are still matched via errors.Is. The expected substrings below are taken from
// the production messages in classifyPluginError.
func TestClassifyPluginError(t *testing.T) {
	const instance = "slack-prod"

	tests := []struct {
		name      string
		err       error
		wantMatch string
		// wantNot guards against a sentinel accidentally falling through to the
		// generic default message ("is unavailable").
		wantNot string
	}{
		{
			name:      "instance not running",
			err:       fmt.Errorf("%w: %q", ErrPluginInstanceNotRunning, instance),
			wantMatch: "not currently available",
			wantNot:   "is unavailable",
		},
		{
			name:      "manager unavailable",
			err:       fmt.Errorf("%w: %q", ErrPluginManagerUnavailable, instance),
			wantMatch: "plugin subsystem is not available",
			wantNot:   "not currently available",
		},
		{
			name:      "call timeout",
			err:       fmt.Errorf("dispatch: %w", ErrPluginCallTimeout),
			wantMatch: "timed out",
		},
		{
			name:      "queue full",
			err:       fmt.Errorf("dispatch: %w", ErrPluginQueueFull),
			wantMatch: "is at capacity",
		},
		{
			name:      "cancelled falls through to generic",
			err:       fmt.Errorf("call cancelled: %w", context.Canceled),
			wantMatch: "is unavailable",
		},
		{
			name:      "generic error falls through to default",
			err:       errors.New("boom"),
			wantMatch: "is unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyPluginError(instance, tt.err)
			if !strings.Contains(got, tt.wantMatch) {
				t.Errorf("classifyPluginError() = %q, want substring %q", got, tt.wantMatch)
			}
			if tt.wantNot != "" && strings.Contains(got, tt.wantNot) {
				t.Errorf("classifyPluginError() = %q, must NOT contain %q (wrong case matched)", got, tt.wantNot)
			}
			// Every message names the instance so the operator/agent knows which
			// plugin failed.
			if !strings.Contains(got, instance) {
				t.Errorf("classifyPluginError() = %q, expected it to name instance %q", got, instance)
			}
		})
	}
}

// TestPluginErrorSentinelsAreDistinct guards the AC requirement that
// ErrInstanceNotRunning (instance down) stays distinguishable from
// ErrManagerUnavailable (subsystem off): they must NOT be the same value, and a
// wrapped one must not match the other under errors.Is.
func TestPluginErrorSentinelsAreDistinct(t *testing.T) {
	if errors.Is(ErrPluginInstanceNotRunning, ErrPluginManagerUnavailable) {
		t.Fatal("ErrPluginInstanceNotRunning and ErrPluginManagerUnavailable must be distinct sentinels")
	}
	wrappedDown := fmt.Errorf("ctx: %w", ErrPluginInstanceNotRunning)
	if errors.Is(wrappedDown, ErrPluginManagerUnavailable) {
		t.Fatal("an instance-not-running error must not match the manager-unavailable sentinel")
	}
	if !errors.Is(wrappedDown, ErrPluginInstanceNotRunning) {
		t.Fatal("errors.Is must unwrap to ErrPluginInstanceNotRunning")
	}
}
