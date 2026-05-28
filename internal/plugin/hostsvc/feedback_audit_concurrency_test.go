package hostsvc_test

// Concurrent WriteAuditStep test — exercises the TOCTOU fix from #348.
//
// The race: two goroutines both call WriteAuditStep(feedback_response) for the
// same run_id at the same time. Before the fix, both could observe the same
// MAX(step_number) and attempt to INSERT with the same (run_id, step_number),
// causing a unique-constraint violation. The fix wraps the
// latestStepNumber+CreateRunStep+UpdateFeedbackRequestStatus in a single
// transaction, so only one writer can hold the write lock at a time.
//
// The test uses a real SQLite DB (not a fakeQuerier) to reproduce the
// production execution environment.

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
)

// openConcurrencyStore opens a temp-file SQLite store for concurrency tests.
func openConcurrencyStore(t *testing.T) (*db.Store, *db.Queries) {
	t.Helper()
	dbPath := t.TempDir() + "/concurrency_test.db"
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	return store, store.Queries()
}

// seedConcurrencyRun inserts the minimal DB rows needed for N concurrent
// WriteAuditStep calls: one plugin, one instance, one policy, one run, and N
// distinct feedback_request rows. Returns the run ID and a slice of feedback
// request IDs.
func seedConcurrencyRun(t *testing.T, q *db.Queries, n int, instanceID, instanceName string) (runID string, feedbackIDs []string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	pluginID := model.NewULID()
	_, err := q.CreatePlugin(ctx, db.CreatePluginParams{
		ID:               pluginID,
		Name:             "concurrency-test-plugin",
		PluginVersion:    "0.1.0",
		ManifestSnapshot: `name: concurrency-test-plugin`,
		TrustedPubkey:    "test-pubkey",
		Status:           "active",
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if err != nil {
		t.Fatalf("CreatePlugin: %v", err)
	}

	_, err = q.CreatePluginInstance(ctx, db.CreatePluginInstanceParams{
		ID:                instanceID,
		PluginID:          pluginID,
		InstanceName:      instanceName,
		ConfigJson:        `{}`,
		HandshakeVersions: `{}`,
		HealthState:       "healthy",
		CreatedAt:         now,
		UpdatedAt:         now,
	})
	if err != nil {
		t.Fatalf("CreatePluginInstance: %v", err)
	}

	policyID := model.NewULID()
	policyYAML := fmt.Sprintf(`
name: concurrency-test-policy
trigger:
  type: manual
capabilities:
  tools:
    - tool: %s.echo
`, instanceName)
	_, err = q.CreatePolicy(ctx, db.CreatePolicyParams{
		ID:          policyID,
		Name:        "concurrency-test-policy",
		TriggerType: "manual",
		Yaml:        policyYAML,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}

	runID = model.NewULID()
	_, err = q.CreateRun(ctx, db.CreateRunParams{
		ID:          runID,
		PolicyID:    policyID,
		Model:       "claude-opus-4-5",
		TriggerType: "manual",
		StartedAt:   now,
		CreatedAt:   now,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	for i := 0; i < n; i++ {
		frID := model.NewULID()
		_, err = q.CreateFeedbackRequest(ctx, db.CreateFeedbackRequestParams{
			ID:        frID,
			RunID:     runID,
			ToolName:  "gleipnir.ask_operator",
			Message:   fmt.Sprintf("concurrent feedback request %d", i),
			CreatedAt: now,
		})
		if err != nil {
			t.Fatalf("CreateFeedbackRequest[%d]: %v", i, err)
		}
		feedbackIDs = append(feedbackIDs, frID)
	}
	return runID, feedbackIDs
}

// TestWriteAuditStep_ConcurrentFeedbackStepsHaveUniqueStepNumbers spawns N
// goroutines each calling WriteAuditStep(feedback_response) for a distinct
// feedback_request on the same run. It asserts that all resulting step_number
// values are unique — the pre-fix race would produce duplicates (constraint
// violation or silent overwrite) under -race.
func TestWriteAuditStep_ConcurrentFeedbackStepsHaveUniqueStepNumbers(t *testing.T) {
	const concurrency = 8

	store, q := openConcurrencyStore(t)
	instanceID := "conc-inst-" + model.NewULID()
	instanceName := "conc-plugin"
	runID, feedbackIDs := seedConcurrencyRun(t, q, concurrency, instanceID, instanceName)

	binder := &fakeInstanceBinder{id: instanceID, ok: true}
	pub := &fakePublisher{}
	// Pass sql.ErrNoRows for GetPluginPendingRequest so all calls take the
	// native (feedback_requests) path.
	fq := &fakeInstanceQuerier{
		store:        store,
		q:            q,
		instanceID:   instanceID,
		instanceName: instanceName,
	}
	srv := hostsvc.NewServer(fq, store.DB(), testEncryptionKey, &fakeResolver{}, binder, pub, nil)

	var wg sync.WaitGroup
	errs := make([]error, concurrency)
	for i := 0; i < concurrency; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			resp, err := srv.WriteAuditStep(ctx, &hostv1.WriteAuditStepRequest{
				StepType:    "feedback_response",
				RequestId:   feedbackIDs[i],
				PayloadJson: fmt.Sprintf(`{"index":%d}`, i),
			})
			if err != nil {
				errs[i] = fmt.Errorf("goroutine %d: RPC error: %w", i, err)
				return
			}
			if !resp.GetOk() {
				errs[i] = fmt.Errorf("goroutine %d: ok=false: %v", i, resp.GetError().GetMessage())
			}
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d failed: %v", i, err)
		}
	}

	// Collect all step_numbers for the run and assert they are unique.
	steps, err := q.ListRunSteps(context.Background(), db.ListRunStepsParams{
		RunID: runID,
		After: -1,
		Limit: int64(concurrency + 10),
	})
	if err != nil {
		t.Fatalf("ListRunSteps: %v", err)
	}
	if len(steps) != concurrency {
		t.Errorf("step count = %d, want %d", len(steps), concurrency)
	}
	seen := make(map[int64]bool, len(steps))
	for _, s := range steps {
		if seen[s.StepNumber] {
			t.Errorf("duplicate step_number %d in run %s", s.StepNumber, runID)
		}
		seen[s.StepNumber] = true
	}
}

// fakeInstanceQuerier wraps a real *db.Queries so WriteAuditStep can look up
// the real DB rows, but intercepts GetPluginPendingRequest to return
// sql.ErrNoRows (forcing the native feedback_requests path) and
// GetPluginInstanceByID to return the seeded instance row.
type fakeInstanceQuerier struct {
	fakeAuditQuerier
	store        *db.Store
	q            *db.Queries
	instanceID   string
	instanceName string
}

func (f *fakeInstanceQuerier) GetPluginInstanceByID(ctx context.Context, id string) (db.PluginInstance, error) {
	return f.q.GetPluginInstanceByID(ctx, id)
}

func (f *fakeInstanceQuerier) UpdatePluginInstanceHealth(ctx context.Context, arg db.UpdatePluginInstanceHealthParams) (int64, error) {
	return f.q.UpdatePluginInstanceHealth(ctx, arg)
}

func (f *fakeInstanceQuerier) GetLatestRunStep(ctx context.Context, runID string) (db.RunStep, error) {
	return f.q.GetLatestRunStep(ctx, runID)
}

func (f *fakeInstanceQuerier) GetFeedbackRequest(ctx context.Context, id string) (db.FeedbackRequest, error) {
	return f.q.GetFeedbackRequest(ctx, id)
}

func (f *fakeInstanceQuerier) UpdateFeedbackRequestStatus(ctx context.Context, arg db.UpdateFeedbackRequestStatusParams) (int64, error) {
	return f.q.UpdateFeedbackRequestStatus(ctx, arg)
}

func (f *fakeInstanceQuerier) CreateRunStep(ctx context.Context, arg db.CreateRunStepParams) (db.RunStep, error) {
	return f.q.CreateRunStep(ctx, arg)
}

func (f *fakeInstanceQuerier) GetRun(ctx context.Context, id string) (db.Run, error) {
	return f.q.GetRun(ctx, id)
}

func (f *fakeInstanceQuerier) GetPolicy(ctx context.Context, id string) (db.Policy, error) {
	return f.q.GetPolicy(ctx, id)
}

// GetPluginPendingRequest always returns sql.ErrNoRows to force the native
// feedback_requests path in WriteAuditStep.
func (f *fakeInstanceQuerier) GetPluginPendingRequest(_ context.Context, _ string) (db.PluginPendingRequest, error) {
	return db.PluginPendingRequest{}, sql.ErrNoRows
}

func (f *fakeInstanceQuerier) GetPluginByID(ctx context.Context, id string) (db.Plugin, error) {
	return f.q.GetPluginByID(ctx, id)
}

func (f *fakeInstanceQuerier) ListPolicies(ctx context.Context) ([]db.Policy, error) {
	return f.q.ListPolicies(ctx)
}

func (f *fakeInstanceQuerier) ListRunsByPolicy(ctx context.Context, arg db.ListRunsByPolicyParams) ([]db.ListRunsByPolicyRow, error) {
	return f.q.ListRunsByPolicy(ctx, arg)
}

func (f *fakeInstanceQuerier) ListRunsByPolicies(ctx context.Context, arg db.ListRunsByPoliciesParams) ([]db.ListRunsByPoliciesRow, error) {
	return f.q.ListRunsByPolicies(ctx, arg)
}

func (f *fakeInstanceQuerier) ListAllActiveUsersWithRoles(ctx context.Context) ([]db.ListAllActiveUsersWithRolesRow, error) {
	return f.q.ListAllActiveUsersWithRoles(ctx)
}

func (f *fakeInstanceQuerier) ListActiveUsersByRole(ctx context.Context, role string) ([]db.ListActiveUsersByRoleRow, error) {
	return f.q.ListActiveUsersByRole(ctx, role)
}

// compile-time check that fakeInstanceQuerier satisfies Querier.
var _ hostsvc.Querier = (*fakeInstanceQuerier)(nil)
