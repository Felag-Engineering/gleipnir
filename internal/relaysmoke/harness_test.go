//go:build relaysmoke

package relaysmoke

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/execution/agent"
	"github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/http/api"
	"github.com/felag-engineering/gleipnir/internal/http/auth"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/policy"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// approverUserID/approverUsername are the identity every h.do request is
// stamped with (auth.WithUserContext), and the users row newHarness seeds.
// "dev-approver" matches the smoke lane's own gate file
// (testdata/approval-gates.json)'s audience entry, so if a future Relay build
// asserts the responder's username against the audience, this identity is
// eligible.
const (
	approverUserID   = "u-relaysmoke-approver"
	approverUsername = "dev-approver"
)

// env is the smoke lane's configuration, read entirely from the environment
// scripts/relaysmoke.sh sets up. Secrets (the admin and approver tokens)
// arrive as FILE paths, never as token values in the environment itself, and
// are never logged.
type env struct {
	MCPURL        string
	APIBase       string
	CAPEM         string
	AdminToken    string
	ApproverToken string
	RelayRef      string
	ReportPath    string
	ExpectedNodes int
}

// loadEnv reads every RELAYSMOKE_* variable the lane needs. A missing
// variable is t.Fatalf, never t.Skip -- a skipped smoke lane is a silent
// green, and the whole point of this suite is that it is never that.
func loadEnv(t *testing.T) env {
	t.Helper()

	get := func(key string) string {
		v := os.Getenv(key)
		if v == "" {
			t.Fatalf("%s is not set -- run this suite via scripts/relaysmoke.sh, not `go test` directly (see docs/developer/relay-smoke.md)", key)
		}
		return v
	}
	readFile := func(key string) string {
		path := get(key)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s (path %s): %v -- run this suite via scripts/relaysmoke.sh (see docs/developer/relay-smoke.md)", key, path, err)
		}
		return strings.TrimSpace(string(b))
	}

	expectedNodesRaw := get("RELAYSMOKE_EXPECTED_NODES")
	expectedNodes, err := strconv.Atoi(expectedNodesRaw)
	if err != nil {
		t.Fatalf("RELAYSMOKE_EXPECTED_NODES=%q is not an integer", expectedNodesRaw)
	}

	return env{
		MCPURL:        get("RELAYSMOKE_MCP_URL"),
		APIBase:       get("RELAYSMOKE_API_BASE"),
		CAPEM:         readFile("RELAYSMOKE_CA_FILE"),
		AdminToken:    readFile("RELAYSMOKE_ADMIN_TOKEN_FILE"),
		ApproverToken: readFile("RELAYSMOKE_APPROVER_TOKEN_FILE"),
		RelayRef:      get("RELAYSMOKE_RELAY_REF"),
		ReportPath:    get("RELAYSMOKE_REPORT"),
		ExpectedNodes: expectedNodes,
	}
}

// report is the smoke run's machine-readable summary, written to
// env.ReportPath from a t.Cleanup registered in the top-level test so it
// lands on a failure too -- scripts/relaysmoke.sh and the CI step summary
// both read it.
type report struct {
	RelayRef        string         `json:"relay_ref"`
	ProtocolVersion string         `json:"protocol_version"`
	Tools           []string       `json:"tools"`
	ReaderNodes     int            `json:"reader_nodes"`
	MRTR            string         `json:"mrtr"`
	RawExecOutcomes map[string]int `json:"raw_exec_outcomes"`
}

func (r *report) writeTo(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("write report to %s: %w", path, err)
	}
	return nil
}

// signalPublisher is an event.Publisher that records every event like
// testutil.RecordingPublisher, and also fans each one out to per-event-type
// subscriber channels. This is the suite's signal-don't-poll seam: a test
// subscribes to "tool_input.created" BEFORE calling launch, so it cannot miss
// the event the agent publishes the instant a pause's DB row commits.
type signalPublisher struct {
	testutil.RecordingPublisher

	mu   sync.Mutex
	subs map[string][]chan json.RawMessage
}

func newSignalPublisher() *signalPublisher {
	return &signalPublisher{subs: make(map[string][]chan json.RawMessage)}
}

// Publish records the event (via the embedded RecordingPublisher) and fans it
// out to every subscriber of eventType. Sends never block: each subscriber
// channel is buffered 16, and a full channel drops the fan-out rather than
// stalling the agent goroutine that called Publish.
func (p *signalPublisher) Publish(eventType string, data json.RawMessage) {
	p.RecordingPublisher.Publish(eventType, data)

	p.mu.Lock()
	subs := append([]chan json.RawMessage(nil), p.subs[eventType]...)
	p.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- data:
		default:
		}
	}
}

// subscribe returns a channel that receives every future eventType event.
// Register a subscription before the action that can produce the event, or
// the event may fire before anyone is listening.
func (p *signalPublisher) subscribe(eventType string) <-chan json.RawMessage {
	ch := make(chan json.RawMessage, 16)
	p.mu.Lock()
	p.subs[eventType] = append(p.subs[eventType], ch)
	p.mu.Unlock()
	return ch
}

// harness is the in-process Gleipnir this suite drives Relay through: a real
// store, RunManager, MCP registry, and the exact HTTP handlers + role
// middleware internal/http/api/router.go wires in production.
type harness struct {
	store    *db.Store
	manager  *run.RunManager
	registry *mcp.Registry
	router   *chi.Mux
	pub      *signalPublisher
	encKey   []byte
	env      env
}

// newHarness builds one harness for the whole TestRelaySmoke run. Every
// subtest shares it; there is no per-subtest store.
func newHarness(t *testing.T, e env) *harness {
	t.Helper()
	ctx := context.Background()

	store := testutil.NewTestStore(t)
	// Registered immediately after NewTestStore so t.Cleanup unwinds LIFO:
	// the manager drains every in-flight run BEFORE the store closes
	// underneath it (CLAUDE.md "Drain launched runs before cleanup").
	manager := run.NewRunManager()
	t.Cleanup(manager.Wait)

	// The identity h.do() stamps into every request -- a real users row so a
	// decision record's actor_user_id (an FK to users) can name it, and named
	// to match the gate's audience entry.
	if _, err := store.CreateUser(ctx, db.CreateUserParams{
		ID:           approverUserID,
		Username:     approverUsername,
		PasswordHash: "x",
		CreatedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("seed approver user: %v", err)
	}

	encKey := make([]byte, 32)
	if _, err := rand.Read(encKey); err != nil {
		t.Fatalf("generate encryption key: %v", err)
	}

	// Deliberately no mcp.WithMCPTimeout here: the registry keeps the
	// instance-wide GLEIPNIR_MCP_TIMEOUT default, exactly as main.go
	// constructs it. Relay's 120s call budget is set per-server through the
	// POST /api/v1/mcp/servers body (issue #939, "call_timeout_seconds") in
	// t.Run("register") -- this lane is also a live check of that feature.
	registry := mcp.NewRegistry(store.Queries(), mcp.WithEncryptionKey(encKey))

	pub := newSignalPublisher()
	runsHandler := run.NewRunsHandler(store, manager, pub)
	mcpHandler := api.NewMCPHandler(store, registry, encKey)

	router := chi.NewRouter()
	router.With(auth.RequireRole(model.RoleAdmin, model.RoleOperator)).
		Post("/api/v1/mcp/servers", mcpHandler.Create)
	router.With(auth.RequireRole(model.RoleOperator, model.RoleApprover, model.RoleAuditor)).
		Get("/api/v1/runs/{runID}/tool-input", runsHandler.GetToolInput)
	router.With(auth.RequireRole(model.RoleApprover, model.RoleOperator)).
		Post("/api/v1/runs/{runID}/tool-input", runsHandler.SubmitToolInput)
	router.With(auth.RequireRole(model.RoleOperator, model.RoleApprover, model.RoleAuditor)).
		Get("/api/v1/runs/{runID}/decisions", runsHandler.ListDecisions)

	return &harness{
		store:    store,
		manager:  manager,
		registry: registry,
		router:   router,
		pub:      pub,
		encKey:   encKey,
		env:      e,
	}
}

// do issues one HTTP request against h.router, stamping the session identity
// authed() (internal/execution/run/tool_input_handler_test.go) uses: the
// seeded approver user, with the given roles. auth.WithUserContext takes
// []string, so roles is converted here rather than asking every caller to
// spell string(role) itself.
func (h *harness) do(t *testing.T, method, path, body string, roles ...model.Role) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	roleStrings := make([]string, len(roles))
	for i, r := range roles {
		roleStrings[i] = string(r)
	}
	req = req.WithContext(auth.WithUserContext(req.Context(), approverUserID, approverUsername, roleStrings))

	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

// launch inserts policyYAML under policyID, parses it, and starts a run
// through a real RunLauncher/RunManager, with an AgentFactory that injects
// llm as the run's LLM client (testutil.NewMockLLMClient) rather than calling
// a real model -- this suite tests the Gleipnir<->Relay integration, not the
// model, and a scripted tool sequence is what makes a failure diagnosable.
func (h *harness) launch(t *testing.T, policyID, policyYAML string, llm *testutil.MockLLMClient) string {
	t.Helper()
	ctx := context.Background()

	parsed, err := policy.Parse(policyYAML, "anthropic", "claude-opus-4-5")
	if err != nil {
		t.Fatalf("policy.Parse(%s): %v", policyID, err)
	}
	testutil.InsertPolicy(t, h.store, policyID, parsed.Name, "manual", policyYAML)

	launcher := run.NewRunLauncher(run.RunLauncherConfig{
		Store:    h.store,
		Resolver: run.NewDefaultToolResolver(h.registry, nil, nil),
		Manager:  h.manager,
		AgentFactory: func(cfg agent.Config) (*agent.BoundAgent, error) {
			cfg.LLMClient = llm
			return agent.New(cfg)
		},
		Publisher: h.pub,
	})

	result, err := launcher.Launch(ctx, run.LaunchParams{
		PolicyID:       policyID,
		TriggerType:    model.TriggerTypeManual,
		TriggerPayload: `{}`,
		ParsedPolicy:   parsed,
	})
	if err != nil {
		t.Fatalf("Launch(%s): %v", policyID, err)
	}
	return result.RunID
}

// awaitDone runs manager.WaitForDeregistration in a goroutine and closes the
// returned channel once the run deregisters. On timeout the channel is never
// closed; the caller's own select carries its own deadline against it, so two
// timeouts are never stacked.
func (h *harness) awaitDone(runID string, d time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		if h.manager.WaitForDeregistration(runID, d) {
			close(done)
		}
	}()
	return done
}

// toolResultStep is one decoded tool_result run step. Output is a STRING
// field (agent.go writes outputStr := string(result.Output)) whose contents
// are themselves JSON -- the MCP content array -- so a caller passes
// []byte(step.Output) to decodeToolPayload, not step.Output directly.
type toolResultStep struct {
	ToolName string `json:"tool_name"`
	Output   string `json:"output"`
	IsError  bool   `json:"is_error"`
}

// toolResults decodes every tool_result step of runID's trace, in step
// order.
func toolResults(t *testing.T, store *db.Store, runID string) []toolResultStep {
	t.Helper()
	steps, err := store.Queries().ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: runID, After: -1, Limit: 1000})
	if err != nil {
		t.Fatalf("ListRunSteps(%s): %v", runID, err)
	}
	var out []toolResultStep
	for _, step := range steps {
		if step.Type != string(model.StepTypeToolResult) {
			continue
		}
		var tr toolResultStep
		if err := json.Unmarshal([]byte(step.Content), &tr); err != nil {
			t.Fatalf("unmarshal tool_result step content %q: %v", step.Content, err)
		}
		out = append(out, tr)
	}
	return out
}

// errorSteps returns the content of every error step in runID's trace, for a
// failure message that names what actually went wrong rather than just "the
// run failed".
func errorSteps(t *testing.T, store *db.Store, runID string) []string {
	t.Helper()
	steps, err := store.Queries().ListRunSteps(context.Background(), db.ListRunStepsParams{RunID: runID, After: -1, Limit: 1000})
	if err != nil {
		t.Fatalf("ListRunSteps(%s): %v", runID, err)
	}
	var out []string
	for _, step := range steps {
		if step.Type == string(model.StepTypeError) {
			out = append(out, step.Content)
		}
	}
	return out
}

// runStatus returns runID's current status column.
func runStatus(t *testing.T, store *db.Store, runID string) string {
	t.Helper()
	r, err := store.Queries().GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun(%s): %v", runID, err)
	}
	return r.Status
}

// diagnoseRegistration maps a discovery_error string (from
// MCPHandler.Create's response) to a named, actionable cause. msg is
// discovery_error's text; url is the server's URL, used only for context in
// the "unclassified" case's own message elsewhere. The three named causes are
// exactly the three deliberately-broken scenarios the acceptance criteria and
// t.Run("negative_controls") exercise: a wrong CA, a wrong token, and an
// unreachable server. These strings come from
// mcp.TLSVerificationError.Error() and mcp.HTTPStatusError.Error() -- kept in
// sync with those two types by hand, since discovery_error is a plain string
// by the time it reaches this response, not a typed error a caller could
// errors.As against.
func diagnoseRegistration(msg string) string {
	switch {
	case strings.Contains(msg, "TLS certificate verification failed"):
		return "CA configuration: the ca_cert_pem registered for Relay does not verify its control-plane certificate"
	case strings.Contains(msg, "status 401"), strings.Contains(msg, "status 403"):
		return "bearer token: Relay rejected the machine Account's API Token"
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "connection refused"):
		return "reachability: is the fleet up, and does `relay` resolve to 127.0.0.1?"
	default:
		return "unclassified"
	}
}
