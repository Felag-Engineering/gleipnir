package policy

import (
	"context"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// fakePoliciesQuerier implements ListPoliciesQuerier for testing without a
// real database.
type fakePoliciesQuerier struct {
	policies []db.Policy
	err      error
}

func (f *fakePoliciesQuerier) ListPolicies(_ context.Context) ([]db.Policy, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.policies, nil
}

func makePolicy(id, name, yamlText string) db.Policy {
	return db.Policy{ID: id, Name: name, Yaml: yamlText}
}

const toolOnlyPolicyYAML = `
trigger:
  type: webhook
capabilities:
  tools:
    - tool: slack-prod.post_message
    - tool: slack-prod.list_channels
`

const subscribedPolicyYAML = `
trigger:
  type: subscribed
  source: slack-prod
  event_kind: message_received
capabilities:
  tools: []
`

const otherPluginPolicyYAML = `
trigger:
  type: webhook
capabilities:
  tools:
    - tool: jira-prod.create_issue
`

const containsButNotStartsWithYAML = `
trigger:
  type: webhook
capabilities:
  tools:
    - tool: my-slack-prod.something
`

const malformedYAML = `
trigger: :::not valid yaml at all
`

func TestScanPolicyToolRefs_EmptyList(t *testing.T) {
	q := &fakePoliciesQuerier{}
	refs, err := ScanPolicyToolRefs(context.Background(), q, []string{"slack-prod."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if refs == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(refs) != 0 {
		t.Errorf("expected 0 refs, got %d", len(refs))
	}
}

func TestScanPolicyToolRefs_ToolPrefixMatch(t *testing.T) {
	q := &fakePoliciesQuerier{
		policies: []db.Policy{
			makePolicy("pol-1", "Slack Policy", toolOnlyPolicyYAML),
		},
	}
	refs, err := ScanPolicyToolRefs(context.Background(), q, []string{"slack-prod."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].ID != "pol-1" {
		t.Errorf("ref ID = %q, want pol-1", refs[0].ID)
	}
	if refs[0].Name != "Slack Policy" {
		t.Errorf("ref Name = %q, want Slack Policy", refs[0].Name)
	}
	// Both tools match the prefix.
	if len(refs[0].Tools) != 2 {
		t.Errorf("expected 2 matched tools, got %d: %v", len(refs[0].Tools), refs[0].Tools)
	}
}

func TestScanPolicyToolRefs_SubscribedTriggerSourceMatch(t *testing.T) {
	q := &fakePoliciesQuerier{
		policies: []db.Policy{
			makePolicy("pol-2", "Subscribed Policy", subscribedPolicyYAML),
		},
	}
	refs, err := ScanPolicyToolRefs(context.Background(), q, []string{"slack-prod."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref (subscribed trigger), got %d", len(refs))
	}
	if refs[0].ID != "pol-2" {
		t.Errorf("ref ID = %q, want pol-2", refs[0].ID)
	}
}

func TestScanPolicyToolRefs_ContainsButNotStartsWith(t *testing.T) {
	// "my-slack-prod.something" contains "slack-prod" but does NOT start with
	// the prefix "slack-prod.", so it must not match.
	q := &fakePoliciesQuerier{
		policies: []db.Policy{
			makePolicy("pol-3", "Other Plugin", containsButNotStartsWithYAML),
		},
	}
	refs, err := ScanPolicyToolRefs(context.Background(), q, []string{"slack-prod."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("expected 0 refs (contains-but-not-starts-with), got %d: %v", len(refs), refs)
	}
}

func TestScanPolicyToolRefs_MalformedYAMLSilentlySkipped(t *testing.T) {
	q := &fakePoliciesQuerier{
		policies: []db.Policy{
			makePolicy("pol-bad", "Broken Policy", malformedYAML),
			makePolicy("pol-1", "Good Policy", toolOnlyPolicyYAML),
		},
	}
	refs, err := ScanPolicyToolRefs(context.Background(), q, []string{"slack-prod."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The malformed policy is skipped; only the good policy matches.
	if len(refs) != 1 {
		t.Errorf("expected 1 ref (malformed skipped), got %d: %v", len(refs), refs)
	}
	if refs[0].ID != "pol-1" {
		t.Errorf("ref ID = %q, want pol-1", refs[0].ID)
	}
}

func TestScanPolicyToolRefs_NoMatchForDifferentPlugin(t *testing.T) {
	q := &fakePoliciesQuerier{
		policies: []db.Policy{
			makePolicy("pol-jira", "Jira Policy", otherPluginPolicyYAML),
		},
	}
	refs, err := ScanPolicyToolRefs(context.Background(), q, []string{"slack-prod."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("expected 0 refs for non-matching plugin, got %d", len(refs))
	}
}

func TestScanPolicyToolRefs_MultiPrefixDedup(t *testing.T) {
	// A policy that matches BOTH prefixes (tool from instance-a AND instance-b)
	// should appear exactly once in the result.
	multiToolYAML := `
trigger:
  type: webhook
capabilities:
  tools:
    - tool: inst-a.do_thing
    - tool: inst-b.do_other
`
	q := &fakePoliciesQuerier{
		policies: []db.Policy{
			makePolicy("pol-multi", "Multi Policy", multiToolYAML),
		},
	}
	refs, err := ScanPolicyToolRefs(context.Background(), q, []string{"inst-a.", "inst-b."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 1 {
		t.Errorf("expected 1 ref (deduped), got %d: %v", len(refs), refs)
	}
	if refs[0].ID != "pol-multi" {
		t.Errorf("ref ID = %q, want pol-multi", refs[0].ID)
	}
	// Both matched tools should appear.
	if len(refs[0].Tools) != 2 {
		t.Errorf("expected 2 matched tools, got %d: %v", len(refs[0].Tools), refs[0].Tools)
	}
}

func TestScanPolicyToolRefsForInstance(t *testing.T) {
	q := &fakePoliciesQuerier{
		policies: []db.Policy{
			makePolicy("pol-1", "Slack Policy", toolOnlyPolicyYAML),
			makePolicy("pol-jira", "Jira Policy", otherPluginPolicyYAML),
		},
	}
	refs, err := ScanPolicyToolRefsForInstance(context.Background(), q, "slack-prod")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].ID != "pol-1" {
		t.Errorf("ref ID = %q, want pol-1", refs[0].ID)
	}
}
