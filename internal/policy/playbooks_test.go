package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/model"
)

// TestPlaybookPolicies_ParseAndValidate parses and validates every policy
// YAML shipped under docs/playbooks/*/*.yaml, the same two steps the policy
// service runs on save. A moved or renamed playbook directory should fail
// loudly rather than pass vacuously, hence the minimum-match assertion.
func TestPlaybookPolicies_ParseAndValidate(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "..", "docs", "playbooks", "*", "*.yaml"))
	if err != nil {
		t.Fatalf("glob playbook yaml files: %v", err)
	}
	if len(matches) < 2 {
		t.Fatalf("found %d playbook yaml files, want at least 2 (fleet-reader.yaml, fleet-responder.yaml); did docs/playbooks move?", len(matches))
	}

	for _, path := range matches {
		rel, err := filepath.Rel(filepath.Join("..", ".."), path)
		if err != nil {
			rel = path
		}
		t.Run(rel, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}

			p, err := Parse(string(raw), "anthropic", "claude-sonnet-4-6")
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if err := Validate(p); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

// TestPlaybookPolicies_FleetOpsGrants pins the grant invariants the fleet-ops
// pitch rests on: the reader's tool set stays at exactly five read-side
// tools, the responder adds only a params-scoped run_operation, and neither
// policy grants raw_exec or approve_request or gates run_operation with
// Gleipnir's own approval.
func TestPlaybookPolicies_FleetOpsGrants(t *testing.T) {
	tests := []struct {
		name               string
		path               string
		wantTrigger        model.TriggerType
		wantWebhookAuth    model.WebhookAuthMode
		wantChecks         []model.PollCheck
		wantTools          []string
		wantFeedbackEnable bool
		wantParams         map[string][]string // tool -> expected params keys (nil if no params block)
	}{
		{
			name:        "fleet-reader",
			path:        filepath.Join("..", "..", "docs", "playbooks", "fleet-ops", "fleet-reader.yaml"),
			wantTrigger: model.TriggerTypeManual,
			wantTools: []string{
				"relay.list_nodes",
				"relay.describe_node",
				"relay.list_operations",
				"relay.run_operation",
				"relay.get_job",
			},
			wantFeedbackEnable: false,
		},
		{
			name:            "fleet-responder",
			path:            filepath.Join("..", "..", "docs", "playbooks", "fleet-ops", "fleet-responder.yaml"),
			wantTrigger:     model.TriggerTypeWebhook,
			wantWebhookAuth: model.WebhookAuthBearer,
			wantChecks: []model.PollCheck{
				{Path: "$.heartbeat.status", Comparator: model.ComparatorEquals, Value: 0},
			},
			wantTools: []string{
				"relay.list_nodes",
				"relay.describe_node",
				"relay.list_operations",
				"relay.get_job",
				"relay.run_operation",
			},
			wantFeedbackEnable: true,
			wantParams: map[string][]string{
				"relay.run_operation": {"selector", "operation", "args", "plan"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("read %s: %v", tt.path, err)
			}
			p, err := Parse(string(raw), "anthropic", "claude-sonnet-4-6")
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if err := Validate(p); err != nil {
				t.Fatalf("Validate: %v", err)
			}

			if p.Trigger.Type != tt.wantTrigger {
				t.Errorf("trigger.type = %q, want %q", p.Trigger.Type, tt.wantTrigger)
			}
			if tt.wantTrigger == model.TriggerTypeWebhook {
				if p.Trigger.WebhookAuth != tt.wantWebhookAuth {
					t.Errorf("trigger.auth = %q, want %q", p.Trigger.WebhookAuth, tt.wantWebhookAuth)
				}
				if len(p.Trigger.Checks) != len(tt.wantChecks) {
					t.Fatalf("len(checks) = %d, want %d", len(p.Trigger.Checks), len(tt.wantChecks))
				}
				for i, want := range tt.wantChecks {
					got := p.Trigger.Checks[i]
					if got.Path != want.Path || got.Comparator != want.Comparator || got.Value != want.Value {
						t.Errorf("checks[%d] = %+v, want %+v", i, got, want)
					}
				}
			}

			tools := toolSet(p)

			gotNames := make([]string, 0, len(tools))
			for name := range tools {
				gotNames = append(gotNames, name)
			}
			if len(gotNames) != len(tt.wantTools) {
				t.Fatalf("tool set = %v, want %v", gotNames, tt.wantTools)
			}
			for _, name := range tt.wantTools {
				grant, ok := tools[name]
				if !ok {
					t.Errorf("tool %q not granted", name)
					continue
				}
				if grant.Approval == model.ApprovalModeRequired {
					t.Errorf("tool %q has Gleipnir approval: required; this pack leaves approval to Relay", name)
				}
			}

			for _, forbidden := range []string{"relay.raw_exec", "relay.approve_request"} {
				if _, ok := tools[forbidden]; ok {
					t.Errorf("tool %q must not be granted", forbidden)
				}
			}

			for toolName, wantKeys := range tt.wantParams {
				grant, ok := tools[toolName]
				if !ok {
					t.Fatalf("tool %q not found for params check", toolName)
				}
				if len(grant.Params) != len(wantKeys) {
					t.Fatalf("%s params = %v, want keys %v", toolName, grant.Params, wantKeys)
				}
				for _, key := range wantKeys {
					if _, ok := grant.Params[key]; !ok {
						t.Errorf("%s params missing key %q", toolName, key)
					}
				}
				if _, ok := grant.Params["selector"]; !ok {
					t.Errorf("%s params must include selector: an omitted selector matches every Node", toolName)
				}
			}

			if p.Capabilities.Feedback.Enabled != tt.wantFeedbackEnable {
				t.Errorf("feedback.enabled = %v, want %v", p.Capabilities.Feedback.Enabled, tt.wantFeedbackEnable)
			}
		})
	}
}

func toolSet(p *model.ParsedPolicy) map[string]model.ToolCapability {
	set := make(map[string]model.ToolCapability, len(p.Capabilities.Tools))
	for _, tc := range p.Capabilities.Tools {
		set[tc.Tool] = tc
	}
	return set
}
