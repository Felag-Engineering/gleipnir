package audience_test

import (
	"errors"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/audience"
)

// helpers to build test rows

func ptr[T any](v T) *T { return &v }

func baseRow(disableInAppFallback int64) db.GetPluginAudienceWithEntriesRow {
	return db.GetPluginAudienceWithEntriesRow{
		AudienceID:           "aud1",
		AudienceName:         "test",
		AudienceVersion:      0,
		AudienceCreatedAt:    "2024-01-01T00:00:00Z",
		AudienceUpdatedAt:    "2024-01-01T00:00:00Z",
		DisableInAppFallback: disableInAppFallback,
	}
}

func entryRow(disableInAppFallback int64, entryID, instanceID string, position, notify, request int64) db.GetPluginAudienceWithEntriesRow {
	row := baseRow(disableInAppFallback)
	row.EntryID = ptr(entryID)
	row.PluginInstanceID = ptr(instanceID)
	row.Position = ptr(position)
	row.Notify = ptr(notify)
	row.Request = ptr(request)
	row.ConfigJson = ptr("{}")
	return row
}

func TestResolve_NilRows_ErrAudienceNotFound(t *testing.T) {
	_, err := audience.Resolve(nil)
	if !errors.Is(err, audience.ErrAudienceNotFound) {
		t.Errorf("expected ErrAudienceNotFound, got %v", err)
	}
}

func TestResolve_EmptyRows_ErrAudienceNotFound(t *testing.T) {
	_, err := audience.Resolve([]db.GetPluginAudienceWithEntriesRow{})
	if !errors.Is(err, audience.ErrAudienceNotFound) {
		t.Errorf("expected ErrAudienceNotFound, got %v", err)
	}
}

// One LEFT-JOIN null row (audience exists, zero entries) + disable=false
// → synthetic entry appended at position 0.
func TestResolve_LeftJoinNullDisableFalse_OneSynthetic(t *testing.T) {
	rows := []db.GetPluginAudienceWithEntriesRow{baseRow(0)}
	entries, err := audience.Resolve(rows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	e := entries[0]
	if !e.Auto {
		t.Error("expected Auto=true for synthetic entry")
	}
	if e.EntryID != audience.InAppEntryID {
		t.Errorf("EntryID = %q, want %q", e.EntryID, audience.InAppEntryID)
	}
}

// One LEFT-JOIN null row + disable=true → empty slice (no entries, no synthetic).
func TestResolve_LeftJoinNullDisableTrue_EmptySlice(t *testing.T) {
	rows := []db.GetPluginAudienceWithEntriesRow{baseRow(1)}
	entries, err := audience.Resolve(rows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("len(entries) = %d, want 0", len(entries))
	}
}

// Two persisted entries + disable=false → 3 entries; synthetic is last with Auto=true.
func TestResolve_TwoEntries_DisableFalse_SyntheticAppended(t *testing.T) {
	rows := []db.GetPluginAudienceWithEntriesRow{
		entryRow(0, "e1", "inst1", 0, 1, 0),
		entryRow(0, "e2", "inst2", 1, 0, 1),
	}
	entries, err := audience.Resolve(rows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(entries))
	}
	last := entries[2]
	if !last.Auto {
		t.Error("last entry should be Auto=true")
	}
	if last.Position != 2 {
		t.Errorf("synthetic position = %d, want 2 (max+1)", last.Position)
	}
}

// Two persisted entries + disable=true → 2 entries; no synthetic.
func TestResolve_TwoEntries_DisableTrue_NoSynthetic(t *testing.T) {
	rows := []db.GetPluginAudienceWithEntriesRow{
		entryRow(1, "e1", "inst1", 0, 1, 0),
		entryRow(1, "e2", "inst2", 1, 0, 1),
	}
	entries, err := audience.Resolve(rows)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if e.Auto {
			t.Error("no entry should be Auto=true when in-app fallback is disabled")
		}
	}
}
