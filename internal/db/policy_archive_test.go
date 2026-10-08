package db

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

const archiveTestTime = "2026-01-01T00:00:00Z"

func createArchiveTestPolicy(t *testing.T, s *Store, id, name, triggerType string) {
	t.Helper()
	if _, err := s.CreatePolicy(context.Background(), CreatePolicyParams{
		ID:          id,
		Name:        name,
		TriggerType: triggerType,
		Yaml:        "trigger: " + triggerType,
		CreatedAt:   archiveTestTime,
		UpdatedAt:   archiveTestTime,
	}); err != nil {
		t.Fatalf("CreatePolicy %s: %v", id, err)
	}
}

func TestArchivePolicy_HidesFromEveryPolicyQuery(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	for id, triggerType := range map[string]string{
		"sched": "scheduled",
		"poll":  "poll",
		"cron":  "cron",
		"sub":   "subscribed",
		"hook":  "webhook",
	} {
		createArchiveTestPolicy(t, s, id, id, triggerType)
		if err := s.ArchivePolicy(ctx, id, archiveTestTime); err != nil {
			t.Fatalf("ArchivePolicy %s: %v", id, err)
		}
	}
	createArchiveTestPolicy(t, s, "live", "live", "webhook")

	t.Run("GetPolicy and GetPolicyByName return ErrNoRows", func(t *testing.T) {
		if _, err := s.GetPolicy(ctx, "hook"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("GetPolicy: got %v, want sql.ErrNoRows", err)
		}
		if _, err := s.GetPolicyByName(ctx, "hook"); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("GetPolicyByName: got %v, want sql.ErrNoRows", err)
		}
	})

	t.Run("GetPolicyIncludingArchived still finds it", func(t *testing.T) {
		got, err := s.GetPolicyIncludingArchived(ctx, "hook")
		if err != nil {
			t.Fatalf("GetPolicyIncludingArchived: %v", err)
		}
		if got.DeletedAt == nil || *got.DeletedAt != archiveTestTime {
			t.Errorf("DeletedAt = %v, want %s", got.DeletedAt, archiveTestTime)
		}
	})

	t.Run("lists and counts exclude it", func(t *testing.T) {
		list, err := s.ListPolicies(ctx)
		if err != nil {
			t.Fatalf("ListPolicies: %v", err)
		}
		if len(list) != 1 || list[0].ID != "live" {
			t.Errorf("ListPolicies = %d rows, want only 'live'", len(list))
		}
		withRun, err := s.ListPoliciesWithLatestRun(ctx)
		if err != nil {
			t.Fatalf("ListPoliciesWithLatestRun: %v", err)
		}
		if len(withRun) != 1 || withRun[0].ID != "live" {
			t.Errorf("ListPoliciesWithLatestRun = %d rows, want only 'live'", len(withRun))
		}
		n, err := s.CountPolicies(ctx)
		if err != nil {
			t.Fatalf("CountPolicies: %v", err)
		}
		if n != 1 {
			t.Errorf("CountPolicies = %d, want 1", n)
		}
	})

	t.Run("no trigger loader returns it", func(t *testing.T) {
		loaders := map[string]func(context.Context) ([]Policy, error){
			"scheduled":  s.GetScheduledActivePolicies,
			"poll":       s.GetPollActivePolicies,
			"cron":       s.GetCronActivePolicies,
			"subscribed": s.GetSubscribedActivePolicies,
		}
		for name, load := range loaders {
			got, err := load(ctx)
			if err != nil {
				t.Fatalf("%s loader: %v", name, err)
			}
			if len(got) != 0 {
				t.Errorf("%s loader returned %d archived policies, want 0", name, len(got))
			}
		}
	})

	t.Run("update and second archive are refused", func(t *testing.T) {
		_, err := s.UpdatePolicy(ctx, UpdatePolicyParams{ID: "hook", Name: "x", TriggerType: "webhook", Yaml: "y", UpdatedAt: archiveTestTime})
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("UpdatePolicy: got %v, want sql.ErrNoRows", err)
		}
		if err := s.ArchivePolicy(ctx, "hook", archiveTestTime); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("second ArchivePolicy: got %v, want sql.ErrNoRows", err)
		}
	})
}

func TestArchivePolicy_ClearsLiveOnlyStateAndKeepsHistory(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	createArchiveTestPolicy(t, s, "pol", "pol", "webhook")

	secret := "ciphertext"
	if err := s.SetPolicyWebhookSecret(ctx, SetPolicyWebhookSecretParams{Ciphertext: &secret, UpdatedAt: archiveTestTime, ID: "pol"}); err != nil {
		t.Fatalf("SetPolicyWebhookSecret: %v", err)
	}
	if err := s.UpsertPollState(ctx, UpsertPollStateParams{PolicyID: "pol", NextPollAt: archiveTestTime, CreatedAt: archiveTestTime, UpdatedAt: archiveTestTime}); err != nil {
		t.Fatalf("UpsertPollState: %v", err)
	}
	for _, stmt := range []string{
		`INSERT INTO trigger_queue(id, policy_id, trigger_type, trigger_payload, position, created_at) VALUES ('q1', 'pol', 'webhook', '{}', 0, 't')`,
		`INSERT INTO runs(id, policy_id, status, trigger_type, trigger_payload, started_at, created_at) VALUES ('r1', 'pol', 'complete', 'webhook', '{}', 't', 't')`,
	} {
		if _, err := s.DB().Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	if err := s.ArchivePolicy(ctx, "pol", archiveTestTime); err != nil {
		t.Fatalf("ArchivePolicy: %v", err)
	}

	count := func(query string) int {
		t.Helper()
		var n int
		if err := s.DB().QueryRow(query).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM trigger_queue WHERE policy_id = 'pol'`); n != 0 {
		t.Errorf("queued triggers = %d, want 0", n)
	}
	if n := count(`SELECT COUNT(*) FROM poll_states WHERE policy_id = 'pol'`); n != 0 {
		t.Errorf("poll_states = %d, want 0", n)
	}
	if n := count(`SELECT COUNT(*) FROM runs WHERE id = 'r1'`); n != 1 {
		t.Errorf("runs = %d, want 1 (history must survive)", n)
	}
	if n := count(`SELECT COUNT(*) FROM policies WHERE id = 'pol' AND webhook_secret_encrypted IS NULL`); n != 1 {
		t.Errorf("webhook secret was not cleared on archive")
	}
	if _, err := s.GetPolicyWebhookSecret(ctx, "pol"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetPolicyWebhookSecret: got %v, want sql.ErrNoRows", err)
	}
}

func TestArchivePolicy_NameIsReusable(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	createArchiveTestPolicy(t, s, "old", "agent", "webhook")

	if _, err := s.CreatePolicy(ctx, CreatePolicyParams{ID: "dup", Name: "agent", TriggerType: "webhook", Yaml: "y", CreatedAt: archiveTestTime, UpdatedAt: archiveTestTime}); err == nil {
		t.Fatal("duplicate live name accepted")
	}
	if err := s.ArchivePolicy(ctx, "old", archiveTestTime); err != nil {
		t.Fatalf("ArchivePolicy: %v", err)
	}
	createArchiveTestPolicy(t, s, "new", "agent", "webhook")

	got, err := s.GetPolicyByName(ctx, "agent")
	if err != nil {
		t.Fatalf("GetPolicyByName: %v", err)
	}
	if got.ID != "new" {
		t.Errorf("GetPolicyByName returned %q, want the live policy 'new'", got.ID)
	}
}
