package google

import (
	"os"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/llm"
)

// TestMain defaults this package's tests to a single attempt (no retry) so
// error-path and network-failure tests do not incur real backoff sleeps. The
// setting feeds both the manual retry loop and the SDK retry count (via
// llm.SDKMaxRetries). Retry-specific tests opt in with their own
// llm.SetDefaultRetryConfig and restore it on cleanup.
func TestMain(m *testing.M) {
	llm.SetDefaultRetryConfig(llm.RetryConfig{MaxAttempts: 1})
	os.Exit(m.Run())
}
