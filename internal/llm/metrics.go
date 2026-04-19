package llm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/rapp992/gleipnir/internal/metrics"
)

// HTTPError is returned by provider clients (notably openaicompat) from error
// paths where the only classification signal is the HTTP status code. The defer
// in CreateMessage uses errors.As to extract the status and pass it to
// ClassifyHTTPStatus. Wrapping with %w keeps the descriptive message while
// embedding the typed error for classification.
type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

var llmRequestDurationSeconds = promauto.With(metrics.Registry()).NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "gleipnir_llm_request_duration_seconds",
		Help:    "LLM API round-trip duration by provider and model.",
		Buckets: metrics.BucketsSlow,
	},
	[]string{metrics.LabelProvider, metrics.LabelModel},
)

var llmErrorsTotal = promauto.With(metrics.Registry()).NewCounterVec(
	prometheus.CounterOpts{
		Name: "gleipnir_llm_errors_total",
		Help: "LLM API failures by provider and error class.",
	},
	[]string{metrics.LabelProvider, metrics.LabelErrorType},
)

var llmTokensTotal = promauto.With(metrics.Registry()).NewCounterVec(
	prometheus.CounterOpts{
		Name: "gleipnir_llm_tokens_total",
		Help: "LLM token throughput by provider, model, and direction.",
	},
	[]string{metrics.LabelProvider, metrics.LabelModel, metrics.LabelDirection},
)

// ObserveRequestDuration records one LLM API call duration observation.
func ObserveRequestDuration(provider, model string, d time.Duration) {
	llmRequestDurationSeconds.WithLabelValues(provider, model).Observe(d.Seconds())
}

// RecordError increments the LLM error counter for the given provider and
// error_type enum value.
func RecordError(provider, errorType string) {
	llmErrorsTotal.WithLabelValues(provider, errorType).Inc()
}

// RecordTokens adds input and output token counts from usage to the LLM token
// counter. ThinkingTokens are intentionally excluded: the spec defines direction
// as input|output only, and Gemini reports thinking tokens separately from
// output tokens — adding them to "output" would double-count on Gemini.
func RecordTokens(provider, model string, usage TokenUsage) {
	if usage.InputTokens > 0 {
		llmTokensTotal.WithLabelValues(provider, model, metrics.DirectionInput).Add(float64(usage.InputTokens))
	}
	if usage.OutputTokens > 0 {
		llmTokensTotal.WithLabelValues(provider, model, metrics.DirectionOutput).Add(float64(usage.OutputTokens))
	}
}

// ClassifyHTTPStatus maps an HTTP status code to the fixed error_type enum.
// A code of 0 indicates a connection-level failure (no HTTP response received).
func ClassifyHTTPStatus(code int) string {
	switch {
	case code == 0:
		return metrics.ErrorTypeConnection
	case code == 401 || code == 403:
		return metrics.ErrorTypeAuth
	case code == 429:
		return metrics.ErrorTypeRateLimit
	case code >= 500:
		return metrics.ErrorTypeServerError
	case code >= 400:
		return metrics.ErrorTypeProtocol
	default:
		return metrics.ErrorTypeProtocol
	}
}

// ClassifyContextError returns (ErrorTypeTimeout, true) when err is
// context.DeadlineExceeded or context.Canceled; otherwise ("", false).
// Providers check this first so a canceled/timed-out request is always
// labeled "timeout" regardless of any nested network error.
func ClassifyContextError(err error) (string, bool) {
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) {
		return metrics.ErrorTypeTimeout, true
	}
	return "", false
}
