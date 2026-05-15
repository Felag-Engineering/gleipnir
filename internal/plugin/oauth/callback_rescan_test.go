package oauth

import (
	"context"
	"errors"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	pluginstate "github.com/felag-engineering/gleipnir/internal/plugin/state"
)

// fakeRescanQuerier holds a fixed list of plugin instances for rescan tests.
type fakeRescanQuerier struct {
	rows []db.PluginInstance
	err  error
}

func (f *fakeRescanQuerier) ListPluginInstancesForCallbackRescan(_ context.Context) ([]db.PluginInstance, error) {
	return f.rows, f.err
}

// fakeRescanHealth records SetHealthState-equivalent calls via
// UpdatePluginInstanceHealth. It also exposes GetPluginInstanceByID so the
// rescan can perform fresh reads (not used in the rescan path, but satisfies
// the pluginstate.Querier interface).
type fakeRescanHealth struct {
	instances     map[string]db.PluginInstance
	healthUpdates []db.UpdatePluginInstanceHealthParams
	casConflict   bool // if true, all UpdatePluginInstanceHealth calls return 0 rows
}

func newFakeRescanHealth(instances ...db.PluginInstance) *fakeRescanHealth {
	m := &fakeRescanHealth{instances: make(map[string]db.PluginInstance)}
	for _, inst := range instances {
		m.instances[inst.ID] = inst
	}
	return m
}

func (f *fakeRescanHealth) GetPluginInstanceByID(_ context.Context, id string) (db.PluginInstance, error) {
	inst, ok := f.instances[id]
	if !ok {
		return db.PluginInstance{}, errors.New("not found")
	}
	return inst, nil
}

func (f *fakeRescanHealth) UpdatePluginInstanceHealth(_ context.Context, arg db.UpdatePluginInstanceHealthParams) (int64, error) {
	f.healthUpdates = append(f.healthUpdates, arg)
	if f.casConflict {
		return 0, nil
	}
	if inst, ok := f.instances[arg.ID]; ok {
		inst.HealthState = arg.HealthState
		inst.Version++
		f.instances[arg.ID] = inst
	}
	return 1, nil
}

var _ pluginstate.Querier = (*fakeRescanHealth)(nil)

// --- helpers ---

func strPtr(s string) *string { return &s }

func inst(id, state string, callbackURL *string) db.PluginInstance {
	return db.PluginInstance{
		ID:                   id,
		PluginID:             id + "-plugin",
		HealthState:          state,
		LastOauthCallbackUrl: callbackURL,
		Version:              1,
	}
}

const testPublicURL = "https://gleipnir.example.com"
const testExpected = testPublicURL + callbackPath

// --- tests ---

func TestCallbackRescan_EmptyPublicURL_ZeroFlagged(t *testing.T) {
	q := &fakeRescanQuerier{
		rows: []db.PluginInstance{
			inst("inst-1", "healthy", strPtr("https://old.example.com"+callbackPath)),
		},
	}
	health := newFakeRescanHealth()
	r := NewCallbackRescanner(q, health, func() string { return "" }, nil)

	flagged, err := r.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: unexpected error: %v", err)
	}
	if flagged != 0 {
		t.Errorf("flagged = %d, want 0 when public_url is empty", flagged)
	}
	if len(health.healthUpdates) != 0 {
		t.Errorf("expected no health updates, got %d", len(health.healthUpdates))
	}
}

func TestCallbackRescan_MatchingURL_NotFlagged(t *testing.T) {
	q := &fakeRescanQuerier{
		rows: []db.PluginInstance{
			inst("inst-1", "healthy", strPtr(testExpected)),
		},
	}
	health := newFakeRescanHealth(q.rows[0])
	r := NewCallbackRescanner(q, health, func() string { return testPublicURL }, nil)

	flagged, err := r.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if flagged != 0 {
		t.Errorf("flagged = %d, want 0 for matching URL", flagged)
	}
}

func TestCallbackRescan_MismatchedURL_Flagged(t *testing.T) {
	oldURL := "https://old.example.com" + callbackPath
	q := &fakeRescanQuerier{
		rows: []db.PluginInstance{
			inst("inst-1", "healthy", strPtr(oldURL)),
		},
	}
	health := newFakeRescanHealth(q.rows[0])
	r := NewCallbackRescanner(q, health, func() string { return testPublicURL }, nil)

	flagged, err := r.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if flagged != 1 {
		t.Errorf("flagged = %d, want 1", flagged)
	}
	if len(health.healthUpdates) != 1 {
		t.Fatalf("expected 1 health update, got %d", len(health.healthUpdates))
	}
	if health.healthUpdates[0].HealthState != string(model.PluginHealthStatePendingReauthorize) {
		t.Errorf("health_state = %q, want %q", health.healthUpdates[0].HealthState, model.PluginHealthStatePendingReauthorize)
	}
}

func TestCallbackRescan_NullCallbackURL_SkippedByQuery(t *testing.T) {
	// The SQL query already filters out rows with NULL last_oauth_callback_url,
	// so the rescan returns nil rows for them. This test validates that the
	// rescan handles empty row lists safely.
	q := &fakeRescanQuerier{rows: nil}
	health := newFakeRescanHealth()
	r := NewCallbackRescanner(q, health, func() string { return testPublicURL }, nil)

	flagged, err := r.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if flagged != 0 {
		t.Errorf("flagged = %d, want 0 with no rows", flagged)
	}
}

func TestCallbackRescan_CrashedWithMismatch_Flagged(t *testing.T) {
	// crashed is an eligible state — instances may re-authorize once fixed.
	oldURL := "https://old.example.com" + callbackPath
	q := &fakeRescanQuerier{
		rows: []db.PluginInstance{
			inst("inst-crash", "crashed", strPtr(oldURL)),
		},
	}
	health := newFakeRescanHealth(q.rows[0])
	r := NewCallbackRescanner(q, health, func() string { return testPublicURL }, nil)

	flagged, err := r.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if flagged != 1 {
		t.Errorf("flagged = %d, want 1 for crashed instance with mismatched URL", flagged)
	}
}

func TestCallbackRescan_CircuitBrokenWithMismatch_Flagged(t *testing.T) {
	oldURL := "https://old.example.com" + callbackPath
	q := &fakeRescanQuerier{
		rows: []db.PluginInstance{
			inst("inst-cb", "circuit_broken", strPtr(oldURL)),
		},
	}
	health := newFakeRescanHealth(q.rows[0])
	r := NewCallbackRescanner(q, health, func() string { return testPublicURL }, nil)

	flagged, err := r.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if flagged != 1 {
		t.Errorf("flagged = %d, want 1 for circuit_broken instance with mismatched URL", flagged)
	}
}

func TestCallbackRescan_CASConflict_Tolerated(t *testing.T) {
	// A CAS conflict in SetHealthState is logged and does not abort the scan.
	oldURL := "https://old.example.com" + callbackPath
	q := &fakeRescanQuerier{
		rows: []db.PluginInstance{
			inst("inst-1", "healthy", strPtr(oldURL)),
		},
	}
	health := newFakeRescanHealth(q.rows[0])
	health.casConflict = true
	r := NewCallbackRescanner(q, health, func() string { return testPublicURL }, nil)

	// Should not return an error even when every CAS write fails.
	flagged, err := r.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: unexpected error: %v", err)
	}
	// CAS conflict means the write did not land, so flagged stays 0.
	if flagged != 0 {
		t.Errorf("flagged = %d, want 0 when CAS conflict is returned", flagged)
	}
}

func TestCallbackRescan_ListError_Propagated(t *testing.T) {
	q := &fakeRescanQuerier{err: errors.New("db down")}
	health := newFakeRescanHealth()
	r := NewCallbackRescanner(q, health, func() string { return testPublicURL }, nil)

	_, err := r.Scan(context.Background())
	if err == nil {
		t.Fatal("expected error from Scan when list fails, got nil")
	}
}

func TestCallbackRescan_MultipleInstances_OnlyMismatchFlagged(t *testing.T) {
	oldURL := "https://old.example.com" + callbackPath
	q := &fakeRescanQuerier{
		rows: []db.PluginInstance{
			inst("inst-match", "healthy", strPtr(testExpected)),
			inst("inst-mismatch", "healthy", strPtr(oldURL)),
		},
	}
	health := newFakeRescanHealth(q.rows...)
	r := NewCallbackRescanner(q, health, func() string { return testPublicURL }, nil)

	flagged, err := r.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if flagged != 1 {
		t.Errorf("flagged = %d, want 1 (only the mismatched instance)", flagged)
	}
	if len(health.healthUpdates) != 1 {
		t.Fatalf("expected 1 health update, got %d", len(health.healthUpdates))
	}
	if health.healthUpdates[0].ID != "inst-mismatch" {
		t.Errorf("health update for %q, want inst-mismatch", health.healthUpdates[0].ID)
	}
}
