//go:build dood && substratev2

package dood_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/llm"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/decision"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// Every policy names a model explicitly, so no default-model setting is read;
// the LLM itself is always the scripted one (scriptedLLM).
const (
	policyProvider = "anthropic"
	policyModel    = "claude-haiku-4-5"
)

// suitePublisher is the event.Publisher the assembly and the run stack publish
// to. It turns the two events the suite synchronizes on into wake-ups: the
// reconciler's per-pass event (WaitConverged) and a run status change
// (WaitRuns), so a waiter re-evaluates exactly when the world could have
// changed rather than on a timer.
type suitePublisher struct {
	// Each has a buffer of one, which coalesces bursts: a waiter only needs to
	// know "at least one happened since I last looked".
	passes chan struct{}
	runs   chan struct{}
}

func newSuitePublisher() *suitePublisher {
	return &suitePublisher{passes: make(chan struct{}, 1), runs: make(chan struct{}, 1)}
}

func (p *suitePublisher) Publish(eventType string, _ json.RawMessage) {
	var ch chan struct{}
	switch eventType {
	case reconciler.EventPassCompleted:
		ch = p.passes
	case "run.status_changed":
		ch = p.runs
	default:
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// scriptedLLM hands every agent a fresh MockLLMClient over the same script, so
// each run sees the script from its first response however many runs a test
// launches.
type scriptedLLM struct {
	mu     sync.Mutex
	script []*llm.MessageResponse
}

// Set replaces the script for runs created from now on.
func (s *scriptedLLM) Set(responses ...*llm.MessageResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script = responses
}

func (s *scriptedLLM) client() llm.LLMClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.script) == 0 {
		return testutil.NewMockLLMClient(testutil.MakeTextResponse("done"))
	}
	return testutil.NewMockLLMClient(append([]*llm.MessageResponse(nil), s.script...)...)
}

// bindLauncher builds the RunLauncher the way main.go does, over the plugin
// inputs the assembly hands it, and binds it to the assembly so the events
// supervisor launches through it. Only the LLM is replaced.
func (h *harness) bindLauncher() {
	d := h.asm.LauncherDeps()
	h.launcher = runpkg.NewRunLauncher(runpkg.RunLauncherConfig{
		Store:    h.store,
		Resolver: runpkg.NewDefaultToolResolver(h.mcpReg, d.ToolClassifier, d.ToolResolver),
		Manager:  h.runs,
		AgentFactory: func(cfg agent.Config) (*agent.BoundAgent, error) {
			cfg.LLMClient = h.llm.client()
			return agent.New(cfg)
		},
		Publisher:          h.pub,
		PluginRegistrar:    d.Registrar,
		PluginDispatcher:   d.Dispatcher,
		ApprovalDispatcher: d.ApprovalDispatcher,
		FeedbackDispatcher: d.FeedbackDispatcher,
		InputChannelRouter: d.InputChannelRouter,
	})
	h.asm.BindLauncher(context.Background(), h.launcher)
}

// InsertPolicy stores a policy as the policies API would, and returns it parsed.
func (h *harness) InsertPolicy(id, name, triggerType, yaml string) *model.ParsedPolicy {
	h.t.Helper()
	parsed, err := policy.Parse(yaml, policyProvider, policyModel)
	if err != nil {
		h.t.Fatalf("parsing policy %s: %v\n%s", name, err, yaml)
	}
	testutil.InsertPolicy(h.t, h.store, id, name, triggerType, yaml)
	return parsed
}

// Runs returns the policy's runs, newest first.
func (h *harness) Runs(policyID string) []db.ListRunsByPolicyRow {
	h.t.Helper()
	runs, err := h.q.ListRunsByPolicy(context.Background(), db.ListRunsByPolicyParams{PolicyID: policyID, Limit: 100})
	if err != nil {
		h.t.Fatalf("listing runs of %s: %v", policyID, err)
	}
	return runs
}

// WaitRuns blocks until ready returns "" for the policy's runs, re-evaluating
// after every run status change. ready returns why the runs are not there yet.
func (h *harness) WaitRuns(policyID string, budget time.Duration, ready func([]db.ListRunsByPolicyRow) string) []db.ListRunsByPolicyRow {
	h.t.Helper()
	deadline := time.After(budget)
	for {
		runs := h.Runs(policyID)
		why := ready(runs)
		if why == "" {
			return runs
		}
		select {
		case <-h.pub.runs:
		case <-deadline:
			h.t.Fatalf("runs of policy %s not ready within %s: %s\nruns: %s", policyID, budget, why, describeRuns(runs))
		}
	}
}

// countStatus counts runs in status.
func countStatus(runs []db.ListRunsByPolicyRow, status model.RunStatus) int {
	n := 0
	for _, r := range runs {
		if r.Status == string(status) {
			n++
		}
	}
	return n
}

func describeRuns(runs []db.ListRunsByPolicyRow) string {
	out := make([]string, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.ID+"="+r.Status)
	}
	return fmt.Sprint(out)
}

// StopPluginContainer stops the instance's running container from outside
// Gleipnir, as a crash or an operator's `docker stop` would. The desired state
// still says running, so the reconciler restarts it.
func (h *harness) StopPluginContainer(instanceID string) container.ContainerID {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()
	info, ok := h.runningContainer(ctx, instanceID)
	if !ok {
		h.t.Fatalf("instance %s has no running container to stop", instanceID)
	}
	if err := h.rt.Stop(ctx, info.ID, 10*time.Second); err != nil {
		h.t.Fatalf("stopping the container of instance %s: %v", instanceID, err)
	}
	return info.ID
}

// CreateApprover creates a user holding the approver role and maps actorID to
// it on instanceID: the per-instance, admin-set identity authorize_actor and
// the task channel adapter resolve.
func (h *harness) CreateApprover(username, instanceID, actorID string) string {
	h.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	user, err := h.q.CreateUser(ctx, db.CreateUserParams{
		ID: "user-" + username, Username: username, PasswordHash: "unused", CreatedAt: now,
	})
	if err != nil {
		h.t.Fatalf("creating user %s: %v", username, err)
	}
	if err := h.q.AssignRole(ctx, db.AssignRoleParams{UserID: user.ID, Role: string(model.RoleApprover), CreatedAt: now}); err != nil {
		h.t.Fatalf("assigning approver to %s: %v", username, err)
	}
	if err := h.q.SetAdminIdentity(ctx, db.SetAdminIdentityParams{
		InstanceID: instanceID, UserID: user.ID, ExternalUserID: actorID, CreatedAt: now,
	}); err != nil {
		h.t.Fatalf("mapping actor %q to %s on instance %s: %v", actorID, username, instanceID, err)
	}
	return user.ID
}

// CreateRequestAudience creates an audience whose only entry is instanceID,
// enabled for requests and delivering directly to address, with the in-app
// fallback turned off so the plugin entry is the only one there is.
//
// The entry's config is the channel-neutral target (spec §4.2). An entry
// without one is skipped as unroutable, and a permission ask with nothing left
// to route to waits on the in-app channel, where nobody in this suite answers.
//
// Written through the queries rather than the audiences API: that API still
// validates entries against the v1 channel manifest shape and has no v2 path
// yet, and the rows are exactly what it would write.
func (h *harness) CreateRequestAudience(name, instanceID, address string) {
	h.t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	aud, err := h.q.CreatePluginAudience(ctx, db.CreatePluginAudienceParams{
		ID: "aud-" + name, Name: name, CreatedAt: now, UpdatedAt: now, DisableInAppFallback: 1,
	})
	if err != nil {
		h.t.Fatalf("creating audience %s: %v", name, err)
	}
	if _, err := h.q.CreateAudienceEntry(ctx, db.CreateAudienceEntryParams{
		ID: "entry-" + name, AudienceID: aud.ID, PluginInstanceID: instanceID,
		Position: 0, Notify: 0, Request: 1,
		ConfigJson: fmt.Sprintf(`{"delivery":"direct","address":%q}`, address),
	}); err != nil {
		h.t.Fatalf("adding instance %s to audience %s: %v", instanceID, name, err)
	}
}

// Decisions returns the run's decision records, the §6.6 evidence the
// decisions endpoint serves.
func (h *harness) Decisions(runID string) []decision.Record {
	h.t.Helper()
	records, err := decision.NewRecorder(h.q).ForRun(context.Background(), runID)
	if err != nil {
		h.t.Fatalf("reading decision records of run %s: %v", runID, err)
	}
	return records
}
