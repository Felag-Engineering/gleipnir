// Package testutil provides shared test helpers for database-backed tests.
// Import it in test files only — it is not part of the production API.
package testutil

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rapp992/gleipnir/internal/db"
	"github.com/rapp992/gleipnir/internal/model"
)

// MinimalWebhookPolicy is the smallest YAML that parses cleanly with trigger
// type webhook and the default concurrency (skip). Shared across test packages
// that need a valid policy to insert without caring about its specific settings.
const MinimalWebhookPolicy = `
name: test-policy
trigger:
  type: webhook
  auth: none
agent:
  model: claude-opus-4-5
  task: "test task"
`

// NewTestStore opens a TempDir-backed SQLite DB, applies the schema, and
// registers cleanup. Using a temp file (not :memory:) ensures WAL mode and
// foreign-key constraints behave identically to production.
func NewTestStore(tb testing.TB) *db.Store {
	tb.Helper()
	s, err := db.Open(filepath.Join(tb.TempDir(), "test.db"))
	if err != nil {
		tb.Fatalf("db.Open: %v", err)
	}
	tb.Cleanup(func() { s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		tb.Fatalf("store.Migrate: %v", err)
	}
	return s
}

// InsertPolicy inserts a policy row with the given id, name, triggerType, and yaml.
func InsertPolicy(tb testing.TB, s *db.Store, id, name, triggerType, yaml string) {
	tb.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.CreatePolicy(context.Background(), db.CreatePolicyParams{
		ID:          id,
		Name:        name,
		TriggerType: triggerType,
		Yaml:        yaml,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		tb.Fatalf("InsertPolicy %s: %v", id, err)
	}
}

// InsertRun inserts a run row with the given id, policyID, and status.
func InsertRun(tb testing.TB, s *db.Store, id, policyID string, status model.RunStatus) {
	tb.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB().Exec(
		`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at)
		 VALUES (?, ?, ?, 'webhook', '{}', ?, ?)`,
		id, policyID, string(status), now, now,
	)
	if err != nil {
		tb.Fatalf("InsertRun %s: %v", id, err)
	}
}

// InsertRunWithTime inserts a run row with specific created_at timestamp and token cost.
func InsertRunWithTime(tb testing.TB, s *db.Store, id, policyID string, status model.RunStatus, createdAt string, tokenCost int64) {
	tb.Helper()
	_, err := s.DB().ExecContext(context.Background(),
		`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at, token_cost)
		 VALUES (?, ?, ?, 'webhook', '{}', ?, ?, ?)`,
		id, policyID, string(status), createdAt, createdAt, tokenCost,
	)
	if err != nil {
		tb.Fatalf("InsertRunWithTime %s: %v", id, err)
	}
}

// InsertRunStep inserts a run_step row with sensible defaults.
func InsertRunStep(tb testing.TB, s *db.Store, id, runID string, stepNumber int64) {
	tb.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.CreateRunStep(context.Background(), db.CreateRunStepParams{
		ID:         id,
		RunID:      runID,
		StepNumber: stepNumber,
		Type:       "thought",
		Content:    "",
		TokenCost:  0,
		CreatedAt:  now,
	})
	if err != nil {
		tb.Fatalf("InsertRunStep %s: %v", id, err)
	}
}

// InsertApprovalRequest inserts an approval_request row with sensible defaults.
func InsertApprovalRequest(tb testing.TB, s *db.Store, id, runID, toolName string) {
	tb.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	expiresAt := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	_, err := s.CreateApprovalRequest(context.Background(), db.CreateApprovalRequestParams{
		ID:               id,
		RunID:            runID,
		ToolName:         toolName,
		ProposedInput:    "",
		ReasoningSummary: "",
		ExpiresAt:        expiresAt,
		CreatedAt:        now,
	})
	if err != nil {
		tb.Fatalf("InsertApprovalRequest %s: %v", id, err)
	}
}

// InsertQueueEntry inserts a trigger_queue row for the given policy and trigger type.
// The payload is '{}' and position is computed automatically by the DB.
func InsertQueueEntry(tb testing.TB, s *db.Store, policyID, triggerType string) {
	tb.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.EnqueueTrigger(context.Background(), db.EnqueueTriggerParams{
		ID:             model.NewULID(),
		PolicyID:       policyID,
		TriggerType:    triggerType,
		TriggerPayload: "{}",
		CreatedAt:      now,
	})
	if err != nil {
		tb.Fatalf("InsertQueueEntry for policy %s: %v", policyID, err)
	}
}

// SetRunVersion directly sets the version column for a run row. Used by tests
// that need to seed a non-zero version to verify CAS semantics.
func SetRunVersion(tb testing.TB, s *db.Store, runID string, version int64) {
	tb.Helper()
	_, err := s.DB().ExecContext(context.Background(),
		`UPDATE runs SET version = ? WHERE id = ?`,
		version, runID,
	)
	if err != nil {
		tb.Fatalf("SetRunVersion %s: %v", runID, err)
	}
}

// InsertMcpServer inserts a minimal MCP server row.
func InsertMcpServer(tb testing.TB, s *db.Store, id, name, url string) {
	tb.Helper()
	_, err := s.CreateMCPServer(context.Background(), db.CreateMCPServerParams{
		ID:        id,
		Name:      name,
		Url:       url,
		CreatedAt: "2024-01-01T00:00:00Z",
	})
	if err != nil {
		tb.Fatalf("InsertMcpServer %s: %v", id, err)
	}
}

// RecordingPublisher is a thread-safe event.Publisher that records all
// published events for test assertions.
type RecordingPublisher struct {
	mu     sync.Mutex
	Events []RecordedEvent
}

// RecordedEvent holds a single captured event type and its raw payload.
type RecordedEvent struct {
	Type string
	Data json.RawMessage
}

// Publish records the event. It satisfies the event.Publisher interface.
func (p *RecordingPublisher) Publish(eventType string, data json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Events = append(p.Events, RecordedEvent{Type: eventType, Data: data})
}

// EventsByType returns all recorded events matching the given type.
func (p *RecordingPublisher) EventsByType(eventType string) []RecordedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []RecordedEvent
	for _, e := range p.Events {
		if e.Type == eventType {
			out = append(out, e)
		}
	}
	return out
}
