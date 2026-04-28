package llm_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
	"github.com/felag-engineering/gleipnir/internal/llm"
)

func TestClassifyHTTPStatus(t *testing.T) {
	tests := []struct {
		code int
		want string
	}{
		{0, "connection"},
		{401, "auth"},
		{403, "auth"},
		{429, "rate_limit"},
		{400, "protocol"},
		{418, "protocol"},
		{500, "server_error"},
		{502, "server_error"},
		{503, "server_error"},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("HTTP %d", tc.code), func(t *testing.T) {
			got := llm.ClassifyHTTPStatus(tc.code)
			if got != tc.want {
				t.Errorf("ClassifyHTTPStatus(%d) = %q, want %q", tc.code, got, tc.want)
			}
		})
	}
}

func TestClassifyContextError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantType  string
		wantMatch bool
	}{
		{
			name:      "DeadlineExceeded",
			err:       context.DeadlineExceeded,
			wantType:  "timeout",
			wantMatch: true,
		},
		{
			name:      "Canceled",
			err:       context.Canceled,
			wantType:  "timeout",
			wantMatch: true,
		},
		{
			name:      "wrapped DeadlineExceeded",
			err:       fmt.Errorf("wrapped: %w", context.DeadlineExceeded),
			wantType:  "timeout",
			wantMatch: true,
		},
		{
			name:      "unrelated error",
			err:       fmt.Errorf("some other error"),
			wantType:  "",
			wantMatch: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := llm.ClassifyContextError(tc.err)
			if ok != tc.wantMatch {
				t.Errorf("ClassifyContextError(%v) matched = %v, want %v", tc.err, ok, tc.wantMatch)
			}
			if got != tc.wantType {
				t.Errorf("ClassifyContextError(%v) type = %q, want %q", tc.err, got, tc.wantType)
			}
		})
	}
}

func TestLLMMetricFamiliesRegistered(t *testing.T) {
	// Import the llm package to trigger its init-time promauto registrations.
	_ = llm.ClassifyHTTPStatus

	want := map[string]bool{
		"gleipnir_llm_request_duration_seconds": false,
		"gleipnir_llm_errors_total":             false,
		"gleipnir_llm_tokens_total":             false,
	}

	ch := make(chan *prometheus.Desc, 1024)
	metrics.Registry().Describe(ch)
	close(ch)

	for d := range ch {
		desc := d.String()
		for name := range want {
			if strings.Contains(desc, `"`+name+`"`) {
				want[name] = true
			}
		}
	}

	for name, found := range want {
		if !found {
			t.Errorf("metric family %q not registered", name)
		}
	}
}
