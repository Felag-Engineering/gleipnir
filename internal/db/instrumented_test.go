package db

// Tests for InstrumentedQueries. This file is in package db (not db_test) so
// it can access the unexported dbQueryDurationSeconds histogram.
//
// internal/testutil cannot be imported here — it imports internal/db, which
// would create an import cycle. The openTestStore helper below inlines the same
// pattern as testutil.NewTestStore.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rapp992/gleipnir/internal/infra/metrics"
)

// openTestStore opens a fresh temp-file SQLite store and applies migrations.
// Using a temp file (not :memory:) ensures WAL and foreign-key constraints
// behave identically to production.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("s.Migrate: %v", err)
	}
	return s
}

// TestInstrumentedQueries_DelegatesAndRecords verifies that each of the six
// wrapped methods (a) delegates correctly and returns the expected row, and (b)
// causes dbQueryDurationSeconds to have at least one registered label series
// after the call (a series only materializes in a HistogramVec after its first
// Observe).
func TestInstrumentedQueries_DelegatesAndRecords(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	s := openTestStore(t)
	iq := NewInstrumentedQueries(s.Queries())

	// Insert prerequisite rows directly into the store so we can exercise
	// GetRun, CreateRunStep, GetPolicy, and GetApprovalRequest.
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO policies(id, name, trigger_type, yaml, created_at, updated_at)
		 VALUES ('pol-1', 'test-policy', 'webhook', '{}', ?, ?)`, now, now,
	); err != nil {
		t.Fatalf("insert policy: %v", err)
	}

	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
		 VALUES ('run-1', 'pol-1', 'pending', 'webhook', '{}', ?, ?)`, now, now,
	); err != nil {
		t.Fatalf("insert run: %v", err)
	}

	t.Run("CreateRun", func(t *testing.T) {
		run, err := iq.CreateRun(ctx, CreateRunParams{
			ID:             "run-cr",
			PolicyID:       "pol-1",
			Model:          "claude-opus-4-5",
			TriggerType:    "manual",
			TriggerPayload: "{}",
			StartedAt:      now,
			CreatedAt:      now,
		})
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		if run.ID != "run-cr" {
			t.Errorf("run.ID = %q, want %q", run.ID, "run-cr")
		}
	})

	t.Run("GetRun", func(t *testing.T) {
		run, err := iq.GetRun(ctx, "run-1")
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if run.ID != "run-1" {
			t.Errorf("run.ID = %q, want %q", run.ID, "run-1")
		}
	})

	t.Run("CreateRunStep", func(t *testing.T) {
		step, err := iq.CreateRunStep(ctx, CreateRunStepParams{
			ID:         "step-1",
			RunID:      "run-1",
			StepNumber: 0,
			Type:       "thought",
			Content:    `{"text":"hello"}`,
			TokenCost:  0,
			CreatedAt:  now,
		})
		if err != nil {
			t.Fatalf("CreateRunStep: %v", err)
		}
		if step.ID != "step-1" {
			t.Errorf("step.ID = %q, want %q", step.ID, "step-1")
		}
	})

	t.Run("ListPolicies", func(t *testing.T) {
		policies, err := iq.ListPolicies(ctx)
		if err != nil {
			t.Fatalf("ListPolicies: %v", err)
		}
		if len(policies) == 0 {
			t.Fatal("ListPolicies returned empty; expected at least pol-1")
		}
	})

	t.Run("GetPolicy", func(t *testing.T) {
		pol, err := iq.GetPolicy(ctx, "pol-1")
		if err != nil {
			t.Fatalf("GetPolicy: %v", err)
		}
		if pol.ID != "pol-1" {
			t.Errorf("pol.ID = %q, want %q", pol.ID, "pol-1")
		}
	})

	t.Run("GetApprovalRequest", func(t *testing.T) {
		// Insert the approval request row first, then retrieve it via the wrapper.
		if _, err := s.DB().ExecContext(ctx,
			`INSERT INTO approval_requests(id, run_id, tool_name, proposed_input, reasoning_summary, status, expires_at, created_at)
			 VALUES ('apr-1', 'run-1', 'some_tool', '{}', 'summary', 'pending', ?, ?)`, now, now,
		); err != nil {
			t.Fatalf("insert approval_request: %v", err)
		}
		apr, err := iq.GetApprovalRequest(ctx, "apr-1")
		if err != nil {
			t.Fatalf("GetApprovalRequest: %v", err)
		}
		if apr.ID != "apr-1" {
			t.Errorf("apr.ID = %q, want %q", apr.ID, "apr-1")
		}
	})

	// After all six wrapped calls, the histogram must have at least one
	// registered label series. CollectAndCount returns series count (not sample
	// count); a series materialises only after its first Observe.
	if promtestutil.CollectAndCount(dbQueryDurationSeconds) == 0 {
		t.Fatalf("expected histogram to have at least one registered label series after call")
	}
}

// TestInstrumentedQueries_PassthroughUnshadowed confirms that methods not
// overridden by InstrumentedQueries (e.g. CreatePolicy) are still callable via
// embedding and produce identical results to calling *Queries directly.
func TestInstrumentedQueries_PassthroughUnshadowed(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	s := openTestStore(t)
	iq := NewInstrumentedQueries(s.Queries())

	pol, err := iq.CreatePolicy(ctx, CreatePolicyParams{
		ID:          "pol-pass",
		Name:        "passthrough-policy",
		TriggerType: "manual",
		Yaml:        "{}",
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		t.Fatalf("CreatePolicy via InstrumentedQueries: %v", err)
	}
	if pol.ID != "pol-pass" {
		t.Errorf("pol.ID = %q, want %q", pol.ID, "pol-pass")
	}
}

// TestDBMetricFamiliesRegistered confirms that gleipnir_db_query_duration_seconds
// is registered on the custom Prometheus registry. Mirrors the pattern in
// internal/mcp/metrics_test.go:TestMCPMetricFamiliesRegistered.
func TestDBMetricFamiliesRegistered(t *testing.T) {
	want := map[string]bool{
		"gleipnir_db_query_duration_seconds": false,
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
