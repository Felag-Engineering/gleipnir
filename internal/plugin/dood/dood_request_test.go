//go:build dood && substratev2

package dood_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// stubActorID is STUB_ACTOR_ID's default: the external id the stub claims when
// it answers a request.
const stubActorID = "conformance-actor"

// requestBudget bounds the run end to end: the approval round trip is a
// channel/request, the stub's authorize_actor call back through the host
// endpoint, and the task poller noticing the completion.
const requestBudget = 2 * time.Minute

func gatedEchoPolicyYAML(tool, audience string) string {
	return fmt.Sprintf(`name: dood-gated-echo
audience: %s
trigger:
  type: manual
capabilities:
  tools:
    - tool: %s
      approval: required
      timeout: 2m
agent:
  task: echo a greeting
  model:
    provider: %s
    name: %s
`, audience, tool, policyProvider, policyModel)
}

// TestDooD_PolicyApprovalRoundTripThroughPluginChannel runs a policy whose only
// tool is approval-gated, with its audience set to a plugin channel. The run
// pauses at the gate, the host sends channel/request to the stub, the stub
// authorizes its actor through the real host endpoint and completes the task,
// and the run goes on to call the tool and finish. The decision record has to
// say who approved, through which channel, and how strongly that channel knows
// them.
func TestDooD_PolicyApprovalRoundTripThroughPluginChannel(t *testing.T) {
	h := newHarness(t)

	pluginID := h.InstallBundle("dood-request")
	instanceID := h.CreateInstance(pluginID, "dood-req")
	h.WaitConverged(instanceID)

	approverID := h.CreateApprover("dood-approver", stubActorID)
	h.CreateRequestAudience("dood-approvers", instanceID, "dood-approver")

	const tool = "dood-req.conformance.echo"
	const greeting = "hello through an approval"
	h.llm.Set(
		testutil.MakeToolCallResponse(tool, "call-1", map[string]any{"message": greeting}),
		testutil.MakeTextResponse("done"),
	)
	parsed := h.InsertPolicy("pol-gated", "dood-gated-echo", string(model.TriggerTypeManual),
		gatedEchoPolicyYAML(tool, "dood-approvers"))

	ctx, cancel := context.WithTimeout(context.Background(), requestBudget)
	defer cancel()
	result, err := h.launcher.LaunchWithConcurrency(ctx, runpkg.LaunchParams{
		PolicyID:       "pol-gated",
		TriggerType:    model.TriggerTypeManual,
		TriggerPayload: `{}`,
		ParsedPolicy:   parsed,
	})
	if err != nil {
		t.Fatalf("launching the run: %v", err)
	}
	if result.Outcome != runpkg.OutcomeLaunched {
		t.Fatalf("launch outcome = %v, want launched", result.Outcome)
	}
	runID := result.RunID

	h.WaitRuns("pol-gated", requestBudget, func(runs []db.ListRunsByPolicyRow) string {
		for _, r := range runs {
			if r.ID != runID {
				continue
			}
			if model.IsTerminalStatus(model.RunStatus(r.Status)) {
				return ""
			}
			return "run is " + r.Status
		}
		return "run not found"
	})
	run, err := h.q.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != string(model.RunStatusComplete) {
		t.Fatalf("run ended %s, want complete (error: %v)", run.Status, run.Error)
	}

	// The gate really paused the run and was settled by the stub's actor: the
	// approval row names the approver the actor maps to.
	approvals, err := h.q.ListApprovalDecidersByRun(ctx, runID)
	if err != nil {
		t.Fatalf("listing approval requests: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("%d approval requests, want 1: the run must have paused exactly once", len(approvals))
	}
	if a := approvals[0]; a.Status != string(model.ApprovalStatusApproved) || a.DecidedBy == nil || *a.DecidedBy != approverID {
		t.Errorf("approval = status %q decided by %v, want approved by %s", a.Status, a.DecidedBy, approverID)
	}

	// The tool ran after the approval, against the plugin, and its result went
	// back to the model.
	steps, err := h.q.ListRunSteps(ctx, db.ListRunStepsParams{RunID: runID, Limit: 1000})
	if err != nil {
		t.Fatalf("listing steps: %v", err)
	}
	var sawResult bool
	for _, s := range steps {
		if s.Type == string(model.StepTypeToolResult) && strings.Contains(s.Content, greeting) {
			sawResult = true
		}
	}
	if !sawResult {
		t.Errorf("no tool_result step carries %q; the gated call did not reach the plugin after approval", greeting)
	}

	// The decision record: who, through which channel, how strongly.
	records := h.Decisions(runID)
	if len(records) != 1 {
		t.Fatalf("%d decision records, want exactly one per settled request: %+v", len(records), records)
	}
	rec := records[0]
	if rec.Outcome != decision.OutcomeAnswered {
		t.Errorf("outcome = %q, want %q", rec.Outcome, decision.OutcomeAnswered)
	}
	if rec.ActorExternalID != stubActorID || rec.ActorUserID != approverID {
		t.Errorf("actor = external %q user %q, want %q resolved to %s", rec.ActorExternalID, rec.ActorUserID, stubActorID, approverID)
	}
	if !rec.LinkMethod.Verified() {
		t.Errorf("link method = %q, want a verified link: the host resolved the actor through its directory", rec.LinkMethod)
	}
	if rec.ChannelAssurance != "authenticated" {
		t.Errorf("channel assurance = %q, want authenticated (the stub's declaration)", rec.ChannelAssurance)
	}
	if rec.ChannelInstance != instanceID || rec.ChannelEntryID != "entry-dood-approvers" {
		t.Errorf("channel = entry %q instance %q, want entry-dood-approvers on %s", rec.ChannelEntryID, rec.ChannelInstance, instanceID)
	}
}
