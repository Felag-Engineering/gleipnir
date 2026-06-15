package llm

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/felag-engineering/gleipnir/internal/infra/metrics"
)

// RetryConfig bounds how transient LLM API failures (HTTP 429 and 5xx) are
// retried. A failed call is retried at most MaxAttempts-1 times; between
// attempts the caller backs off, honoring a provider-supplied Retry-After hint
// when present and otherwise growing exponentially from InitialBackoff up to
// MaxBackoff.
type RetryConfig struct {
	MaxAttempts    int           // total attempts including the first; <=1 disables retry
	InitialBackoff time.Duration // base wait for the exponential path (no Retry-After)
	MaxBackoff     time.Duration // ceiling for any single wait, including Retry-After
}

// DefaultRetryConfig governs the manual retry loop used by the providers whose
// SDKs do NOT retry themselves (Google genai, openaicompat). The Anthropic and
// OpenAI SDKs retry internally; for those, only MaxAttempts feeds through (via
// SDKMaxRetries) — InitialBackoff/MaxBackoff are the SDK's own concern.
//
// It is a package-level var (not a constant) so main.go can override it once at
// startup from environment configuration via SetDefaultRetryConfig — the same
// boot-time-injection convention used for the package clock elsewhere. The
// defaults are deliberately conservative: a transient TPM/overload blip is
// smoothed over a few seconds without letting a wedged provider stall a run for
// minutes (the run context is still the hard upper bound).
var DefaultRetryConfig = RetryConfig{
	MaxAttempts:    4,
	InitialBackoff: 1 * time.Second,
	MaxBackoff:     30 * time.Second,
}

// SetDefaultRetryConfig overrides DefaultRetryConfig. Call once at boot, before
// any runs start; it is not safe to call concurrently with in-flight requests.
func SetDefaultRetryConfig(cfg RetryConfig) {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	DefaultRetryConfig = cfg
}

// SDKMaxRetries returns the retry count to configure on provider SDKs that do
// their own retrying (Anthropic, OpenAI), derived from DefaultRetryConfig so the
// GLEIPNIR_LLM_RETRY_MAX_ATTEMPTS knob is uniform across providers. MaxAttempts
// counts the initial try, so SDK retries = MaxAttempts - 1 (floored at 0).
func SDKMaxRetries() int {
	if n := DefaultRetryConfig.MaxAttempts - 1; n > 0 {
		return n
	}
	return 0
}

// IsRetryableNetworkError reports whether err is a transient network-level
// failure (no HTTP response was received) worth retrying — a dial failure, DNS
// error, connection reset, or read/write timeout. Context cancellation and
// deadline-exceeded are explicitly NOT retryable: they mean the caller or the
// run's overall deadline ended the request, and retrying would fight that.
func IsRetryableNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// net.Error is implemented by *net.OpError, *net.DNSError, and *url.Error,
	// so a single interface check covers dial/DNS/reset/timeout failures.
	var netErr net.Error
	return errors.As(err, &netErr)
}

// RetryDecision is returned by a provider's classifier for one failed attempt.
// Retry reports whether the error is transient and worth another attempt;
// RetryAfter carries the provider's wait hint (0 means "use computed backoff");
// ErrorType labels the retry counter so retries are observable per error class.
type RetryDecision struct {
	Retry      bool
	RetryAfter time.Duration
	ErrorType  string
}

// RetryClassifier inspects a failed attempt's error and decides whether to
// retry. Each provider supplies its own, because the typed SDK error and the
// location of the Retry-After hint differ per provider.
type RetryClassifier func(error) RetryDecision

// retrySleep and retryRand are indirection points so tests can drive the
// backoff loop deterministically without wall-clock waits (CLAUDE.md clock
// convention). timeNow backs the HTTP-date branch of Retry-After parsing.
var (
	retrySleep = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			return nil
		}
	}
	retryRand = rand.Float64
	timeNow   = time.Now
)

var llmRetriesTotal = promauto.With(metrics.Registry()).NewCounterVec(
	prometheus.CounterOpts{
		Name: "gleipnir_llm_retries_total",
		Help: "LLM API retries by provider and error class.",
	},
	[]string{metrics.LabelProvider, metrics.LabelErrorType},
)

// RecordRetry increments the retry counter for the given provider and
// error_type. Called once per retry decision, before the backoff wait.
func RecordRetry(provider, errorType string) {
	llmRetriesTotal.WithLabelValues(provider, errorType).Inc()
}

// DoWithRetry runs op up to cfg.MaxAttempts times. After a failed attempt it
// asks classify whether the error is transient; if so it records the retry,
// backs off (honoring the provider's Retry-After hint, capped at MaxBackoff),
// and tries again. The context bounds the total wait: a cancellation or timeout
// during backoff stops the loop and returns the last API error — not the sleep
// error — so the caller always sees the real failure cause.
func DoWithRetry(ctx context.Context, cfg RetryConfig, provider string, classify RetryClassifier, op func() error) error {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil {
			return nil
		}
		if attempt >= cfg.MaxAttempts {
			return err
		}
		decision := classify(err)
		if !decision.Retry {
			return err
		}
		RecordRetry(provider, decision.ErrorType)
		if sleepErr := retrySleep(ctx, backoffDuration(cfg, attempt, decision.RetryAfter)); sleepErr != nil {
			return err
		}
	}
}

// backoffDuration computes the wait before the next attempt. A provider
// Retry-After hint is authoritative (clamped to MaxBackoff as a safety valve
// against pathological server values). Otherwise it applies truncated
// exponential backoff with full jitter: the uncapped target grows as
// InitialBackoff * 2^(attempt-1), is capped at MaxBackoff, then the actual wait
// is drawn uniformly from [0, cap].
//
// Full jitter (rather than a narrow ±band) is the key defense against a
// reconnect storm: when many runs fail at the same instant — e.g. a provider
// drops every in-flight connection — spreading their retries across the whole
// window decorrelates them, instead of having them all wake together and
// re-stampede. Connection-error retries always take this path (they carry no
// Retry-After).
func backoffDuration(cfg RetryConfig, attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if cfg.MaxBackoff > 0 && retryAfter > cfg.MaxBackoff {
			return cfg.MaxBackoff
		}
		return retryAfter
	}

	base := cfg.InitialBackoff
	if base <= 0 {
		base = time.Second
	}
	shift := attempt - 1
	if shift > 20 { // cap the shift to avoid Duration overflow
		shift = 20
	}
	d := base * (1 << shift)
	if cfg.MaxBackoff > 0 && (d > cfg.MaxBackoff || d <= 0) {
		d = cfg.MaxBackoff
	}
	// Full jitter: uniform in [0, d].
	return time.Duration(retryRand() * float64(d))
}

// RetryDecisionForStatus is the single source of truth for which HTTP status
// codes are worth retrying, applied identically by every provider client. It
// takes an already-parsed Retry-After (0 if none) rather than headers so the
// openaicompat client — which captures the hint at response time — and the
// SDK-based clients can both delegate here.
//
// The policy distinguishes transient failures (retry) from permanent ones
// (give up immediately — retrying cannot change the outcome):
//
//	408 Request Timeout      → retry (timeout)      — the request never completed
//	429 Too Many Requests    → retry (rate_limit)   — TPM/RPM; honor Retry-After
//	500 Internal Server Error→ retry (server_error) — transient server fault
//	502 Bad Gateway          → retry (server_error)
//	503 Service Unavailable  → retry (server_error) — often carries Retry-After
//	504 Gateway Timeout      → retry (server_error)
//	529 Overloaded (Anthropic)→ retry (server_error) — transient overload
//	other 5xx (520–527 CDN…) → retry (server_error) — transient gateway faults
//
//	501, 505, 506, 510, 511  → no retry — permanent server/config errors
//	400, 401, 403, 404, 413,
//	422, other 4xx           → no retry — bad request, auth, or context overflow;
//	                                       deterministic, so a retry just burns budget
func RetryDecisionForStatus(code int, retryAfter time.Duration) RetryDecision {
	switch {
	case code == 408:
		return RetryDecision{Retry: true, RetryAfter: retryAfter, ErrorType: metrics.ErrorTypeTimeout}
	case code == 429:
		return RetryDecision{Retry: true, RetryAfter: retryAfter, ErrorType: metrics.ErrorTypeRateLimit}
	case code == 501 || code == 505 || code == 506 || code == 510 || code == 511:
		// Permanent 5xx: the server will not implement/support this request, so
		// retrying is pointless. Excluded before the general 5xx case below.
		return RetryDecision{}
	case code >= 500:
		return RetryDecision{Retry: true, RetryAfter: retryAfter, ErrorType: metrics.ErrorTypeServerError}
	default:
		// All other 4xx (and anything < 408) are deterministic client errors.
		return RetryDecision{}
	}
}

// ParseRetryAfter extracts a retry delay from response headers, preferring the
// millisecond-precision "retry-after-ms" header (sent by Anthropic and OpenAI)
// and falling back to the standard "Retry-After" header in either delta-seconds
// or HTTP-date form. Returns 0 when no usable hint is present. A nil header is
// safe (Get returns "" on a nil map).
func ParseRetryAfter(h http.Header) time.Duration {
	if ms := strings.TrimSpace(h.Get("retry-after-ms")); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}

	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(timeNow()); d > 0 {
			return d
		}
	}
	return 0
}
