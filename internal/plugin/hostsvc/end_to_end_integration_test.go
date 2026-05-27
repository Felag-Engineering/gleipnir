//go:build unix

// End-to-end integration test for hostsvc.Server activation. It spawns a real
// plugin subprocess (via the test binary re-exec pattern), exchanges the full
// go-plugin handshake + broker flow, and round-trips a WriteAuditStep RPC
// through all three interceptors to a real SQLite DB row + event publication.
//
// Build tag: unix — go-plugin uses Unix domain sockets in multiplexed mode.

package hostsvc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/dispatch"
	"github.com/felag-engineering/gleipnir/internal/plugin/generation"
	"github.com/felag-engineering/gleipnir/internal/plugin/hostsvc"
	"github.com/felag-engineering/gleipnir/internal/plugin/identity"
	"github.com/felag-engineering/gleipnir/internal/plugin/process"
	bootstrapv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/bootstrap/v1"
	handshakev1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/handshake/v1"
	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostwire"
	sdkproto "github.com/felag-engineering/gleipnir/plugin-sdk/proto"
)

// ── fixture subprocess mode ─────────────────────────────────────────────────

// runHostsvcFixtureWriteAuditStep runs as the plugin subprocess in
// "serve-and-writeauditstep" fixture mode. It: serves the go-plugin handshake,
// connects to the host's HostService via the broker, calls WriteAuditStep, and
// writes the result to GLEIPNIR_TEST_RESULT_PATH.
//
// Called by TestMain (in process_token_integration_test.go) when
// GLEIPNIR_TEST_FIXTURE == "serve-and-writeauditstep".
func runHostsvcFixtureWriteAuditStep() {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM)

	impl := &writeAuditStepImpl{}
	done := make(chan struct{})
	go func() {
		goplugin.Serve(&goplugin.ServeConfig{
			HandshakeConfig: hostwire.HandshakeConfig,
			Plugins: goplugin.PluginSet{
				"gleipnir": &writeAuditStepGRPCPlugin{impl: impl},
			},
			GRPCServer: goplugin.DefaultGRPCServer,
		})
		close(done)
	}()

	select {
	case <-quit:
	case <-done:
	}
}

// writeAuditStepGRPCPlugin adapts writeAuditStepImpl to the go-plugin
// GRPCPlugin interface and stashes the broker for Bind to use.
type writeAuditStepGRPCPlugin struct {
	goplugin.Plugin
	impl *writeAuditStepImpl
}

func (p *writeAuditStepGRPCPlugin) GRPCServer(broker *goplugin.GRPCBroker, s *grpc.Server) error {
	// Stash the broker so Bind can Dial the host-side HostService.
	p.impl.broker = broker
	handshakev1.RegisterHandshakeServiceServer(s, p.impl)
	bootstrapv1.RegisterBootstrapServiceServer(s, p.impl)
	return nil
}

func (p *writeAuditStepGRPCPlugin) GRPCClient(_ context.Context, _ *goplugin.GRPCBroker, _ *grpc.ClientConn) (interface{}, error) {
	return nil, status.Error(codes.Unimplemented, "GRPCClient not used on plugin side")
}

// writeAuditStepImpl implements the minimum surface for the go-plugin handshake
// and dials the host's HostService to call WriteAuditStep.
type writeAuditStepImpl struct {
	handshakev1.UnimplementedHandshakeServiceServer
	bootstrapv1.UnimplementedBootstrapServiceServer

	broker *goplugin.GRPCBroker
}

func (f *writeAuditStepImpl) Negotiate(_ context.Context, _ *handshakev1.NegotiateRequest) (*handshakev1.NegotiateResponse, error) {
	return &handshakev1.NegotiateResponse{
		SdkVersion:    "0.0.0-writeauditstep-fixture",
		PluginVersion: "0.1.0",
		Ok:            true,
	}, nil
}

// Bind dials the host's HostService and calls WriteAuditStep. The result
// (ok or error string) is written to GLEIPNIR_TEST_RESULT_PATH so the
// test process can assert it without a separate out-of-band channel.
func (f *writeAuditStepImpl) Bind(_ context.Context, req *bootstrapv1.BindRequest) (*bootstrapv1.BindResponse, error) {
	resultPath := os.Getenv("GLEIPNIR_TEST_RESULT_PATH")
	callID := os.Getenv("GLEIPNIR_TEST_CALL_ID")
	requestID := os.Getenv("GLEIPNIR_TEST_FEEDBACK_REQUEST_ID")
	stepType := os.Getenv("GLEIPNIR_TEST_STEP_TYPE")
	if stepType == "" {
		stepType = "feedback_response"
	}

	conn, err := f.broker.Dial(req.GetHostBrokerId())
	if err != nil {
		writeResult(resultPath, false, fmt.Sprintf("broker.Dial: %v", err))
		return &bootstrapv1.BindResponse{Ok: true}, nil
	}
	defer conn.Close()

	// Build outgoing gRPC metadata. GLEIPNIR_TEST_SKIP_TOKEN=1 omits the instance
	// token entirely so the token interceptor's missing-token path is triggered.
	// (We cannot override GLEIPNIR_INSTANCE_TOKEN to empty because process.Start
	// appends the real token as the last env entry, winning over our override.)
	var md metadata.MD
	if os.Getenv("GLEIPNIR_TEST_SKIP_TOKEN") == "1" {
		// No instance token — interceptor will return Unauthenticated.
		md = metadata.Pairs(sdkproto.CallIDMetadataKey, callID)
	} else {
		token := os.Getenv("GLEIPNIR_INSTANCE_TOKEN")
		md = metadata.Pairs(
			sdkproto.InstanceTokenMetadataKey, token,
			sdkproto.CallIDMetadataKey, callID,
		)
	}
	ctx := metadata.NewOutgoingContext(context.Background(), md)

	client := hostv1.NewHostServiceClient(conn)
	resp, rpcErr := client.WriteAuditStep(ctx, &hostv1.WriteAuditStepRequest{
		StepType:    stepType,
		RequestId:   requestID,
		PayloadJson: `{"source":"e2e-fixture"}`,
	})

	if rpcErr != nil {
		writeResult(resultPath, false, rpcErr.Error())
	} else {
		writeResult(resultPath, resp.GetOk(), "")
	}

	return &bootstrapv1.BindResponse{Ok: true}, nil
}

// writeResult serialises the outcome to the result file path. Non-fatal: if the
// path is empty or the write fails, the test will time out polling the file.
func writeResult(path string, ok bool, errMsg string) {
	if path == "" {
		return
	}
	type result struct {
		Ok  bool   `json:"ok"`
		Err string `json:"err,omitempty"`
	}
	b, _ := json.Marshal(result{Ok: ok, Err: errMsg})
	_ = os.WriteFile(path, b, 0o600)
}

// ── fake CallContextResolver ─────────────────────────────────────────────────

// fakeCallResolver satisfies hostsvc.CallContextResolver with a fixed mapping.
// Using a fake avoids the need for a real in-flight call registered in
// dispatch.Pool (which has no public RegisterCall API — deliberate constraint).
type fakeCallResolver struct {
	callID   string
	runID    string
	policyID string
}

func (r *fakeCallResolver) LookupCall(callID string) (dispatch.CallInfo, bool) {
	if callID == r.callID {
		return dispatch.CallInfo{RunID: r.runID, PolicyID: r.policyID}, true
	}
	return dispatch.CallInfo{}, false
}

// ── counting publisher ────────────────────────────────────────────────────────

// countingPublisher records Publish calls for assertion. The mutex guards
// concurrent writes — future parallel tests could call Publish from multiple
// goroutines, and a race detector run would flag the unsynchronised slice
// append even when the result-file poll provides happens-before in practice.
type countingPublisher struct {
	mu       sync.Mutex
	events   []string
	payloads []json.RawMessage
}

func (p *countingPublisher) Publish(eventType string, data json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, eventType)
	p.payloads = append(p.payloads, data)
}

// snapshot returns a copy of recorded events and payloads under the lock.
func (p *countingPublisher) snapshot() (events []string, payloads []json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	events = make([]string, len(p.events))
	copy(events, p.events)
	payloads = make([]json.RawMessage, len(p.payloads))
	copy(payloads, p.payloads)
	return
}

// ── interceptor counter helper ────────────────────────────────────────────────

// countingInterceptor wraps another interceptor and increments a counter each
// time it is called. Used to assert all three chain members observed each RPC.
func countingInterceptor(inner grpc.UnaryServerInterceptor, n *atomic.Int32) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		n.Add(1)
		return inner(ctx, req, info, handler)
	}
}

// ── DB seed helpers ───────────────────────────────────────────────────────────

// seedE2EData inserts the minimal set of DB rows needed for WriteAuditStep to
// succeed: plugin → instance → policy → run → feedback_request. The instanceID
// parameter must match the process.Config.InstanceID so the token interceptor
// resolves to the correct DB row. Returns the feedback request ID.
func seedE2EData(t *testing.T, q *db.Queries, instanceID, instanceName string) (runID, policyID, feedbackRequestID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	pluginID := model.NewULID()
	_, err := q.CreatePlugin(ctx, db.CreatePluginParams{
		ID:               pluginID,
		Name:             "e2e-test-plugin",
		PluginVersion:    "0.1.0",
		ManifestSnapshot: `name: e2e-test-plugin`,
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

	policyID = model.NewULID()
	// The policy YAML must grant at least one <instanceName>.<tool> capability so
	// the WriteAuditStep request_id scope check passes (policyGrantsInstance).
	policyYAML := fmt.Sprintf(`
name: e2e-policy
trigger:
  type: manual
capabilities:
  tools:
    - tool: %s.echo
`, instanceName)
	_, err = q.CreatePolicy(ctx, db.CreatePolicyParams{
		ID:          policyID,
		Name:        "e2e-policy",
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

	feedbackRequestID = model.NewULID()
	_, err = q.CreateFeedbackRequest(ctx, db.CreateFeedbackRequestParams{
		ID:        feedbackRequestID,
		RunID:     runID,
		ToolName:  "gleipnir.ask_operator",
		Message:   "e2e test feedback request",
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("CreateFeedbackRequest: %v", err)
	}

	return runID, policyID, feedbackRequestID
}

// ── e2e test helpers ──────────────────────────────────────────────────────────

// startWriteAuditStepFixture spawns a "serve-and-writeauditstep" fixture
// subprocess using process.Start with the full interceptor chain and a real
// *hostsvc.Server. instanceID must already be registered with the generation
// controller before this function is called, because the fixture calls
// WriteAuditStep from inside Bootstrap.Bind — i.e. before process.Start
// returns. env holds extra KEY=VALUE variables injected before hostwire.Launch.
func startWriteAuditStepFixture(
	t *testing.T,
	instanceID string,
	hostSvc *hostsvc.Server,
	reg *identity.Registry,
	interceptors []grpc.UnaryServerInterceptor,
	env []string,
) *process.Instance {
	t.Helper()

	cfg := process.Config{
		BinaryPath:         os.Args[0],
		InstanceID:         instanceID,
		PluginID:           "e2e-plugin",
		InstanceName:       "e2e-instance",
		StartupTimeout:     30 * time.Second,
		StopGrace:          10 * time.Second,
		IdentityIssuer:     reg,
		HostServer:         hostSvc,
		ServerInterceptors: interceptors,
		Launch: func(ctx context.Context, binaryPath string, host hostwire.HostServer, opts hostwire.Options) (*hostwire.Client, func(), error) {
			// Inject fixture mode and test-specific env vars via opts.Env so the
			// subprocess receives them through the hostwire env allowlist mechanism
			// rather than via os.Setenv on the host process.
			opts.Env = append(opts.Env, "GLEIPNIR_TEST_FIXTURE=serve-and-writeauditstep")
			opts.Env = append(opts.Env, env...)
			return hostwire.Launch(ctx, binaryPath, host, opts)
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	inst, err := process.Start(ctx, cfg)
	if err != nil {
		t.Fatalf("process.Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = inst.Stop(stopCtx)
		<-inst.Done()
	})
	return inst
}

// pollResultFile polls path until it exists or deadline is exceeded. Returns the
// content or fails the test.
func pollResultFile(t *testing.T, path string, deadline time.Duration) []byte {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		data, err := os.ReadFile(path)
		if err == nil {
			return data
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("result file %q not written within %s", path, deadline)
	return nil
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestHostsvcE2E_WriteAuditStep_HappyPath is the primary end-to-end test. It:
//  1. Opens a real SQLite DB and seeds the necessary rows.
//  2. Constructs a *hostsvc.Server with a fakeCallResolver and all three
//     interceptors wrapped in counting shims.
//  3. Spawns a fixture subprocess that calls WriteAuditStep(feedback_response).
//  4. Polls the result file and asserts: ok=true, DB row inserted, event
//     published, all three interceptors traversed exactly once.
func TestHostsvcE2E_WriteAuditStep_HappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: subprocess re-exec")
	}

	store, q := openE2EStore(t)
	defer store.Close()

	// Pre-allocate the instance ID so the DB row and generation controller can
	// both be seeded with the same ID before the subprocess launches. The fixture
	// calls WriteAuditStep from inside Bootstrap.Bind, which fires before
	// process.Start returns.
	instanceID := "e2e-happy-" + model.NewULID()
	runID, policyID, feedbackRequestID := seedE2EData(t, q, instanceID, "e2e-instance")

	fabricatedCallID := "call-" + model.NewULID()
	resolver := &fakeCallResolver{
		callID:   fabricatedCallID,
		runID:    runID,
		policyID: policyID,
	}
	pub := &countingPublisher{}
	reg := identity.New()
	genCtrl := generation.New()
	genCtrl.RegisterInstance(instanceID)

	// Wrap each interceptor in a counter to verify all three are traversed.
	var tokenCount, genCount, callIDCount atomic.Int32
	interceptors := []grpc.UnaryServerInterceptor{
		countingInterceptor(hostsvc.UnaryInstanceTokenInterceptor(reg), &tokenCount),
		countingInterceptor(hostsvc.UnaryGenerationRefcountInterceptor(genCtrl), &genCount),
		countingInterceptor(hostsvc.UnaryCallIDInterceptor(), &callIDCount),
	}

	hostSvc := hostsvc.NewServer(q, testEncryptionKey, resolver, hostsvc.NewContextBinder(), pub, nil)

	resultPath := t.TempDir() + "/result.json"
	env := []string{
		"GLEIPNIR_TEST_CALL_ID=" + fabricatedCallID,
		"GLEIPNIR_TEST_FEEDBACK_REQUEST_ID=" + feedbackRequestID,
		"GLEIPNIR_TEST_RESULT_PATH=" + resultPath,
	}

	inst := startWriteAuditStepFixture(t, instanceID, hostSvc, reg, interceptors, env)

	raw := pollResultFile(t, resultPath, 30*time.Second)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	_ = inst.Stop(stopCtx)

	// Parse the result file.
	var result struct {
		Ok  bool   `json:"ok"`
		Err string `json:"err"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("parse result file: %v", err)
	}
	if !result.Ok || result.Err != "" {
		t.Fatalf("WriteAuditStep RPC failed: ok=%v err=%q", result.Ok, result.Err)
	}

	// Assert a feedback_response run_step was inserted.
	step, err := q.GetLatestRunStep(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetLatestRunStep: %v", err)
	}
	if step.Type != "feedback_response" {
		t.Errorf("run step type = %q, want %q", step.Type, "feedback_response")
	}

	// Blocking #1: assert the publisher recorded at least one event.
	// Blocking #2: assert the event payload references the seeded run_id and
	// instance_id, proving the handler resolved the correct instance end-to-end.
	pubEvents, pubPayloads := pub.snapshot()
	if len(pubEvents) == 0 {
		t.Fatal("publisher recorded no events; WriteAuditStep should publish plugin.feedback_response_written on success")
	}
	foundFeedbackEvent := false
	for i, ev := range pubEvents {
		if ev != "plugin.feedback_response_written" {
			continue
		}
		foundFeedbackEvent = true
		var payload map[string]string
		if err := json.Unmarshal(pubPayloads[i], &payload); err != nil {
			t.Fatalf("unmarshal event payload: %v", err)
		}
		if payload["run_id"] != runID {
			t.Errorf("event payload run_id = %q, want %q", payload["run_id"], runID)
		}
		if payload["instance_id"] != instanceID {
			t.Errorf("event payload instance_id = %q, want %q", payload["instance_id"], instanceID)
		}
	}
	if !foundFeedbackEvent {
		t.Errorf("no plugin.feedback_response_written event recorded; got events: %v", pubEvents)
	}

	// Assert all three interceptors ran.
	if tokenCount.Load() != 1 {
		t.Errorf("token interceptor count = %d, want 1", tokenCount.Load())
	}
	if genCount.Load() != 1 {
		t.Errorf("generation interceptor count = %d, want 1", genCount.Load())
	}
	if callIDCount.Load() != 1 {
		t.Errorf("call-id interceptor count = %d, want 1", callIDCount.Load())
	}
}

// TestHostsvcE2E_WriteAuditStep_WrongStepType verifies that a fixture sending
// step_type="thought" (not "feedback_response") is rejected with PermissionDenied
// and that the interceptor chain was still traversed (all three counters == 1).
func TestHostsvcE2E_WriteAuditStep_WrongStepType(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: subprocess re-exec")
	}

	store, q := openE2EStore(t)
	defer store.Close()

	instanceID := "e2e-wrongtype-" + model.NewULID()
	runID, policyID, feedbackRequestID := seedE2EData(t, q, instanceID, "e2e-wrongtype")

	fabricatedCallID := "call-" + model.NewULID()
	resolver := &fakeCallResolver{
		callID:   fabricatedCallID,
		runID:    runID,
		policyID: policyID,
	}
	pub := &countingPublisher{}
	reg := identity.New()
	genCtrl := generation.New()
	genCtrl.RegisterInstance(instanceID)

	var tokenCount, genCount, callIDCount atomic.Int32
	interceptors := []grpc.UnaryServerInterceptor{
		countingInterceptor(hostsvc.UnaryInstanceTokenInterceptor(reg), &tokenCount),
		countingInterceptor(hostsvc.UnaryGenerationRefcountInterceptor(genCtrl), &genCount),
		countingInterceptor(hostsvc.UnaryCallIDInterceptor(), &callIDCount),
	}

	hostSvc := hostsvc.NewServer(q, testEncryptionKey, resolver, hostsvc.NewContextBinder(), pub, nil)

	resultPath := t.TempDir() + "/result.json"
	env := []string{
		"GLEIPNIR_TEST_CALL_ID=" + fabricatedCallID,
		"GLEIPNIR_TEST_FEEDBACK_REQUEST_ID=" + feedbackRequestID,
		"GLEIPNIR_TEST_RESULT_PATH=" + resultPath,
		"GLEIPNIR_TEST_STEP_TYPE=thought",
	}

	inst := startWriteAuditStepFixture(t, instanceID, hostSvc, reg, interceptors, env)

	raw := pollResultFile(t, resultPath, 30*time.Second)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	_ = inst.Stop(stopCtx)

	var result struct {
		Ok  bool   `json:"ok"`
		Err string `json:"err"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("parse result file: %v", err)
	}
	// The RPC should be rejected: the error string must contain PermissionDenied.
	if result.Ok {
		t.Error("expected RPC to fail for wrong step_type, but ok=true")
	}
	if result.Err == "" {
		t.Error("expected non-empty error for wrong step_type")
	}

	// Blocking #3: assert the handler wrote an unauthorized_step_type audit event.
	// The handler calls writeAuditEvent before returning PermissionDenied, so the
	// row must exist even though the RPC failed.
	auditRows, err := q.ListPluginAuditEventsByType(context.Background(), db.ListPluginAuditEventsByTypeParams{
		EventType: "unauthorized_step_type",
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListPluginAuditEventsByType: %v", err)
	}
	if len(auditRows) == 0 {
		t.Error("expected at least one unauthorized_step_type plugin_audit_events row")
	}

	// Interceptors must still have all fired (the rejection happens in the handler,
	// after all three interceptors ran).
	if tokenCount.Load() != 1 {
		t.Errorf("token interceptor count = %d, want 1", tokenCount.Load())
	}
	if genCount.Load() != 1 {
		t.Errorf("generation interceptor count = %d, want 1", genCount.Load())
	}
	if callIDCount.Load() != 1 {
		t.Errorf("call-id interceptor count = %d, want 1", callIDCount.Load())
	}
}

// TestHostsvcE2E_WriteAuditStep_MissingToken verifies that omitting the instance
// token causes the token interceptor to short-circuit the chain with
// Unauthenticated and the downstream interceptors never run.
func TestHostsvcE2E_WriteAuditStep_MissingToken(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: subprocess re-exec")
	}

	store, q := openE2EStore(t)
	defer store.Close()

	instanceID := "e2e-notoken-" + model.NewULID()
	runID, policyID, feedbackRequestID := seedE2EData(t, q, instanceID, "e2e-notoken")

	fabricatedCallID := "call-" + model.NewULID()
	resolver := &fakeCallResolver{
		callID:   fabricatedCallID,
		runID:    runID,
		policyID: policyID,
	}
	pub := &countingPublisher{}
	reg := identity.New()
	genCtrl := generation.New()
	genCtrl.RegisterInstance(instanceID)

	var tokenCount, genCount, callIDCount atomic.Int32
	interceptors := []grpc.UnaryServerInterceptor{
		countingInterceptor(hostsvc.UnaryInstanceTokenInterceptor(reg), &tokenCount),
		countingInterceptor(hostsvc.UnaryGenerationRefcountInterceptor(genCtrl), &genCount),
		countingInterceptor(hostsvc.UnaryCallIDInterceptor(), &callIDCount),
	}

	hostSvc := hostsvc.NewServer(q, testEncryptionKey, resolver, hostsvc.NewContextBinder(), pub, nil)

	resultPath := t.TempDir() + "/result.json"
	env := []string{
		"GLEIPNIR_TEST_CALL_ID=" + fabricatedCallID,
		"GLEIPNIR_TEST_FEEDBACK_REQUEST_ID=" + feedbackRequestID,
		"GLEIPNIR_TEST_RESULT_PATH=" + resultPath,
		// Tell the fixture to omit the instance token from its RPC metadata.
		// (We cannot override GLEIPNIR_INSTANCE_TOKEN to empty because
		// process.Start appends the real token last, and the last value wins.)
		"GLEIPNIR_TEST_SKIP_TOKEN=1",
	}

	inst := startWriteAuditStepFixture(t, instanceID, hostSvc, reg, interceptors, env)

	raw := pollResultFile(t, resultPath, 30*time.Second)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	_ = inst.Stop(stopCtx)

	var result struct {
		Ok  bool   `json:"ok"`
		Err string `json:"err"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("parse result file: %v", err)
	}
	if result.Ok {
		t.Error("expected RPC to fail for missing token, but ok=true")
	}
	// The error string must be non-empty (error codes cross process boundaries
	// as strings, so we check presence rather than gRPC code equality).
	if result.Err == "" {
		t.Error("expected non-empty error string for missing token")
	}

	// Token interceptor must have run (it is the one that rejects the call).
	if tokenCount.Load() != 1 {
		t.Errorf("token interceptor count = %d, want 1", tokenCount.Load())
	}
	// Generation and call-id interceptors must NOT run (token short-circuited).
	if genCount.Load() != 0 {
		t.Errorf("generation interceptor count = %d, want 0 (short-circuit)", genCount.Load())
	}
	if callIDCount.Load() != 0 {
		t.Errorf("call-id interceptor count = %d, want 0 (short-circuit)", callIDCount.Load())
	}
}

// ── plugin-substrate integration tests ───────────────────────────────────────

// TestHostsvcE2E_WriteAuditStep_PluginSubstrate_Late verifies that when a
// plugin_pending_requests row exists with status='resolved', WriteAuditStep
// returns ok=false and emits a feedback_response_late audit event at severity
// "warning" without touching run state.
func TestHostsvcE2E_WriteAuditStep_PluginSubstrate_Late(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: subprocess re-exec")
	}

	store, q := openE2EStore(t)
	defer store.Close()

	instanceID := "e2e-ps-late-" + model.NewULID()
	runID, policyID, _ := seedE2EData(t, q, instanceID, "e2e-ps-instance")

	// Insert a plugin_pending_requests row with status='resolved'.
	pluginReqID := model.NewULID()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := q.CreatePluginPendingRequest(ctx, db.CreatePluginPendingRequestParams{
		ID:               pluginReqID,
		PluginInstanceID: instanceID,
		RunID:            runID,
		ToolName:         "ask",
		CreatedAt:        now,
	}); err != nil {
		t.Fatalf("CreatePluginPendingRequest: %v", err)
	}
	// Advance to resolved so the handler treats it as late.
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE plugin_pending_requests SET status='resolved', resolved_at=? WHERE id=?`,
		now, pluginReqID,
	); err != nil {
		t.Fatalf("set resolved: %v", err)
	}

	fabricatedCallID := "call-" + model.NewULID()
	resolver := &fakeCallResolver{callID: fabricatedCallID, runID: runID, policyID: policyID}
	pub := &countingPublisher{}
	reg := identity.New()
	genCtrl := generation.New()
	genCtrl.RegisterInstance(instanceID)

	var tokenCount, genCount, callIDCount atomic.Int32
	interceptors := []grpc.UnaryServerInterceptor{
		countingInterceptor(hostsvc.UnaryInstanceTokenInterceptor(reg), &tokenCount),
		countingInterceptor(hostsvc.UnaryGenerationRefcountInterceptor(genCtrl), &genCount),
		countingInterceptor(hostsvc.UnaryCallIDInterceptor(), &callIDCount),
	}

	// fakeChannelResolver returns (false, nil) simulating a scanner-conflict
	// (row resolved, waiter gone).
	ch := &fakeChannelResolver{resolved: false, err: nil}
	hostSvc := hostsvc.NewServer(q, testEncryptionKey, resolver, hostsvc.NewContextBinder(), pub, ch)

	resultPath := t.TempDir() + "/result.json"
	env := []string{
		"GLEIPNIR_TEST_CALL_ID=" + fabricatedCallID,
		"GLEIPNIR_TEST_FEEDBACK_REQUEST_ID=" + pluginReqID,
		"GLEIPNIR_TEST_RESULT_PATH=" + resultPath,
	}

	inst := startWriteAuditStepFixture(t, instanceID, hostSvc, reg, interceptors, env)
	raw := pollResultFile(t, resultPath, 30*time.Second)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	_ = inst.Stop(stopCtx)

	var result struct {
		Ok  bool   `json:"ok"`
		Err string `json:"err"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	// ok=false: late callback
	if result.Ok {
		t.Error("ok = true, want false for late plugin callback")
	}

	// Audit event must exist with severity "warning".
	auditRows, err := q.ListPluginAuditEventsByType(ctx, db.ListPluginAuditEventsByTypeParams{
		EventType: hostsvc.EventTypeFeedbackResponseLate,
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListPluginAuditEventsByType: %v", err)
	}
	if len(auditRows) == 0 {
		t.Error("expected feedback_response_late audit event")
	}
	for _, row := range auditRows {
		if row.Severity != "warning" {
			t.Errorf("severity = %q, want warning", row.Severity)
		}
	}

	// Run state must be unchanged (no run_steps for this request).
	step, stepErr := q.GetLatestRunStep(ctx, runID)
	if stepErr == nil && step.Type == "feedback_response" {
		t.Error("feedback_response run_step written on late callback; want none")
	}
}

// TestHostsvcE2E_WriteAuditStep_PluginSubstrate_Happy verifies the happy path:
// a plugin_pending_requests row with status='pending' causes the handler to
// call Resolve, which (on success) returns ok=true and publishes an SSE event.
func TestHostsvcE2E_WriteAuditStep_PluginSubstrate_Happy(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: subprocess re-exec")
	}

	store, q := openE2EStore(t)
	defer store.Close()

	instanceID := "e2e-ps-happy-" + model.NewULID()
	runID, policyID, _ := seedE2EData(t, q, instanceID, "e2e-ps-happy-instance")

	pluginReqID := model.NewULID()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := q.CreatePluginPendingRequest(ctx, db.CreatePluginPendingRequestParams{
		ID:               pluginReqID,
		PluginInstanceID: instanceID,
		RunID:            runID,
		ToolName:         "ask",
		CreatedAt:        now,
	}); err != nil {
		t.Fatalf("CreatePluginPendingRequest: %v", err)
	}

	fabricatedCallID := "call-" + model.NewULID()
	resolver := &fakeCallResolver{callID: fabricatedCallID, runID: runID, policyID: policyID}
	pub := &countingPublisher{}
	reg := identity.New()
	genCtrl := generation.New()
	genCtrl.RegisterInstance(instanceID)

	var tokenCount, genCount, callIDCount atomic.Int32
	interceptors := []grpc.UnaryServerInterceptor{
		countingInterceptor(hostsvc.UnaryInstanceTokenInterceptor(reg), &tokenCount),
		countingInterceptor(hostsvc.UnaryGenerationRefcountInterceptor(genCtrl), &genCount),
		countingInterceptor(hostsvc.UnaryCallIDInterceptor(), &callIDCount),
	}

	// Fake resolver simulates CAS win.
	ch := &fakeChannelResolver{resolved: true, err: nil}
	hostSvc := hostsvc.NewServer(q, testEncryptionKey, resolver, hostsvc.NewContextBinder(), pub, ch)

	resultPath := t.TempDir() + "/result.json"
	env := []string{
		"GLEIPNIR_TEST_CALL_ID=" + fabricatedCallID,
		"GLEIPNIR_TEST_FEEDBACK_REQUEST_ID=" + pluginReqID,
		"GLEIPNIR_TEST_RESULT_PATH=" + resultPath,
	}

	inst := startWriteAuditStepFixture(t, instanceID, hostSvc, reg, interceptors, env)
	raw := pollResultFile(t, resultPath, 30*time.Second)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	_ = inst.Stop(stopCtx)

	var result struct {
		Ok  bool   `json:"ok"`
		Err string `json:"err"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if !result.Ok {
		t.Errorf("ok = false, err = %q; want ok=true for happy-path plugin substrate", result.Err)
	}

	// SSE event must have been published.
	pubEvents, _ := pub.snapshot()
	found := false
	for _, ev := range pubEvents {
		if ev == "plugin.feedback_response_written" {
			found = true
		}
	}
	if !found {
		t.Errorf("no plugin.feedback_response_written event; got %v", pubEvents)
	}

	// No feedback_response_late event must have been emitted.
	auditRows, err := q.ListPluginAuditEventsByType(ctx, db.ListPluginAuditEventsByTypeParams{
		EventType: hostsvc.EventTypeFeedbackResponseLate,
		Offset:    0,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("ListPluginAuditEventsByType: %v", err)
	}
	if len(auditRows) != 0 {
		t.Errorf("unexpected feedback_response_late events: %d", len(auditRows))
	}
}

// ── store helper ─────────────────────────────────────────────────────────────

// openE2EStore opens a temp-file SQLite store and returns both the store and
// the underlying *db.Queries. The store is closed via t.Cleanup.
func openE2EStore(t *testing.T) (*db.Store, *db.Queries) {
	t.Helper()
	dbPath := t.TempDir() + "/e2e_test.db"
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
