//go:build substratev2

package assembly

// This file boots the whole v2 assembly against container.Fake and one fake
// plugin, and drives it end to end:
//
//	create an instance -> reconcile / attach / observe passes -> the plugin
//	calls the host endpoint with its first-boot token -> a managed tool call
//	through DefaultToolResolver -> an event launches a run -> clean shutdown
//
// Everything is the production composition. What is faked is the world around
// it: the container runtime, the plugin process, and the run launcher at the
// seam the trigger dispatcher calls.
//
// The fake plugin is a real HTTP server, and the "container" is addressed at
// 127.0.0.1. The subnet pool is 127.83.0.0/16 rather than the default
// 10.83.0.0/16 for one reason: Gleipnir binds its host-endpoint and egress
// listeners to the .2 address of each instance's subnet, and on Linux every
// address in 127.0.0.0/8 is bindable, so the listeners can be real.
//
// Synchronization is on published events and channels only. The reconciler
// runs hourly (withSlowReconciler); the test advances it with Kick and waits
// for the observe-pass event each pass publishes.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	runpkg "github.com/felag-engineering/gleipnir/internal/execution/run"
	"github.com/felag-engineering/gleipnir/internal/infra/netguard"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/loader/loadertest"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
)

const (
	e2ePool         = "127.83.0.0/16"
	e2ePluginName   = "acme-notify"
	e2eInstanceName = "acme-notify-1"
	e2eEventKind    = "message.posted"
)

// e2eManifest is the consented v2 manifest: a tool provider and an event
// source attesting one kind, served on port, pinning the image digest.
func e2eManifest(port int, digest string) string {
	return `schema_version: "2"
name: ` + e2ePluginName + `
version: "1.0.0"
package:
  registry_type: oci
  identifier: ghcr.io/acme/notify@` + digest + `
  transport:
    type: streamable-http
    port: ` + strconv.Itoa(port) + `
gleipnir:
  profiles:
    tool_provider: {}
    event_source: {}
  event_kinds:
    - kind: ` + e2eEventKind + `
      description: A message was posted.
`
}

// signalPublisher records events like testutil.RecordingPublisher and also
// announces each event's type on a channel, so a test can wait for "the next
// observe pass finished" instead of sleeping.
type signalPublisher struct {
	testutil.RecordingPublisher
	types chan string
}

func newSignalPublisher() *signalPublisher {
	return &signalPublisher{types: make(chan string, 4096)}
}

func (p *signalPublisher) Publish(eventType string, data json.RawMessage) {
	p.RecordingPublisher.Publish(eventType, data)
	select {
	case p.types <- eventType:
	default:
		// 4096 undelivered events means nothing is waiting on this channel.
	}
}

// drain discards every event announced so far.
func (p *signalPublisher) drain() {
	for {
		select {
		case <-p.types:
		default:
			return
		}
	}
}

// await blocks until an event of eventType is announced.
func (p *signalPublisher) await(t *testing.T, eventType string) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case got := <-p.types:
			if got == eventType {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for the %s event", eventType)
		}
	}
}

// fakePlugin serves the MCP surface of one managed plugin on a single URL, the
// way a real container does: tools from FakeMCPServer, discovery and the events
// stream from FakeEventsServer.
type fakePlugin struct {
	tools  *mcp.FakeMCPServer
	events *mcp.FakeEventsServer
}

func newFakePlugin() *fakePlugin {
	events := mcp.NewFakeEventsServer()
	events.HeartbeatMs = 50
	events.Kinds = []mcp.EventKind{{Kind: e2eEventKind}}
	events.ListenEvents = []mcp.CloudEvent{{
		SpecVersion: "1.0", Source: e2ePluginName, Type: e2eEventKind, ID: "evt-1", Sequence: 1,
		Data: json.RawMessage(`{"channel":"general"}`),
	}}
	return &fakePlugin{
		tools: mcp.NewFakeMCPServer(
			mcp.WithFakeRejectLegacyHandshake(),
			mcp.WithFakeTools(mcp.Tool{Name: "echo", Description: "echoes", InputSchema: json.RawMessage(`{"type":"object"}`)}),
		),
		events: events,
	}
}

func (p *fakePlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var req struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &req)
	switch req.Method {
	case "tools/list", "tools/call":
		p.tools.ServeHTTP(w, r)
	default:
		p.events.ServeHTTP(w, r)
	}
}

// containerEnv captures the environment each plugin container was created with
// and gives it the address the fake plugin listens on, standing in for the
// network the real daemon would attach it to.
type containerEnv struct {
	*container.Fake

	mu   sync.Mutex
	envs map[string][]string // by instance ID
}

func (c *containerEnv) Create(ctx context.Context, opts container.CreateOptions) (container.ContainerID, error) {
	id, err := c.Fake.Create(ctx, opts)
	if err != nil {
		return "", err
	}
	if instanceID := opts.Labels[reconciler.LabelInstance]; instanceID != "" {
		c.Fake.SetIPAddress(id, "127.0.0.1")
		c.mu.Lock()
		if c.envs == nil {
			c.envs = make(map[string][]string)
		}
		c.envs[instanceID] = opts.Env
		c.mu.Unlock()
	}
	return id, nil
}

func (c *containerEnv) envOf(instanceID, name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, kv := range c.envs[instanceID] {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}
	return ""
}

// recordingLauncher is the RunLauncher the trigger dispatcher calls. The real
// one needs an LLM; the seam under test is that an event arrives here.
type recordingLauncher struct {
	launched chan runpkg.LaunchParams
}

func (l *recordingLauncher) LaunchWithConcurrency(_ context.Context, p runpkg.LaunchParams) (runpkg.LaunchResult, error) {
	l.launched <- p
	return runpkg.LaunchResult{RunID: "run-1", Outcome: runpkg.OutcomeLaunched}, nil
}

type e2eFixture struct {
	t        *testing.T
	a        *Assembly
	deps     Deps
	q        *db.Queries
	pub      *signalPublisher
	rt       *containerEnv
	plugin   *fakePlugin
	arbiter  *toolregistry.Registry
	pluginID string
	// key signed the installed bundle; a later release signed with it is an
	// upgrade, not a key rotation.
	key *loadertest.Key
	// pluginAddr is host:port of the fake plugin server.
	pluginAddr string
	handler    http.Handler
}

// newE2EFixture composes the assembly over a fake runtime with an installed,
// approved v2 plugin. Nothing is started: the caller creates an instance, then boots.
func newE2EFixture(t *testing.T) *e2eFixture {
	t.Helper()
	ctx := context.Background()

	plugin := newFakePlugin()
	srv := httptest.NewServer(plugin)
	t.Cleanup(srv.Close)
	pluginAddr := strings.TrimPrefix(srv.URL, "http://")
	_, portStr, err := net.SplitHostPort(pluginAddr)
	if err != nil {
		t.Fatalf("parsing plugin address %q: %v", srv.URL, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("plugin port %q: %v", portStr, err)
	}

	pub := newSignalPublisher()
	deps := newDeps(t, &pub.RecordingPublisher)
	deps.Publisher = pub
	deps.Config.PluginSubnetPool = e2ePool
	q := deps.Store.Queries()
	arbiter := toolregistry.New()
	deps.Arbiter = arbiter
	deps.MCPRegistry = mcp.NewRegistry(q, mcp.WithToolNamespaceArbiter(arbiter))

	fake := container.NewFake()
	selfID, err := fake.Create(ctx, container.CreateOptions{
		Name:    "gleipnir",
		Image:   "gleipnir/gleipnir:test",
		Network: "gleipnir-self-net",
		CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Sysctls: container.RequiredSysctls(),
	})
	if err != nil {
		t.Fatalf("creating gleipnir's own container: %v", err)
	}
	rt := &containerEnv{Fake: fake}

	a, err := New(ctx, deps,
		withDetectConfig(container.DetectConfig{Mode: "docker", DockerSocketPath: "/var/run/docker.sock", DockerSocketExists: true}),
		withRuntime(rt), withSelfContainerID(selfID), withNoForwarding(), withSlowReconciler())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(a.Shutdown)

	// The plugin arrives the way an operator's does: a signed bundle through the
	// installer the upload route and the watcher share, which lands it awaiting
	// review. The fake runtime yields the image the manifest pins.
	key := loadertest.NewKey(t)
	bundle := loadertest.Build(t, loadertest.Options{
		Name: e2ePluginName, Version: "1.0.0", Seed: "e2e-image", Key: key,
		Manifest: func(digest string) []byte { return []byte(e2eManifest(port, digest)) },
	})
	fake.PendingImages = []container.ImageInfo{{ID: bundle.Digest}}
	pluginID, err := a.AdminDeps().Installer.Install(ctx, bundle.Path)
	if err != nil {
		t.Fatalf("installing the bundle: %v", err)
	}
	if fake.Loads != 1 {
		t.Fatalf("image loads = %d, want 1: the install must load the image into the runtime", fake.Loads)
	}
	pluginRow, err := q.GetPluginByID(ctx, pluginID)
	if err != nil {
		t.Fatalf("GetPluginByID: %v", err)
	}
	if pluginRow.Status != string(model.PluginStatusPendingReview) {
		t.Fatalf("installed plugin status = %q, want %q", pluginRow.Status, model.PluginStatusPendingReview)
	}

	f := &e2eFixture{t: t, a: a, deps: deps, q: q, pub: pub, rt: rt, plugin: plugin, arbiter: arbiter, pluginID: pluginID, key: key, pluginAddr: pluginAddr}
	f.handler = f.adminRouter()

	// Approval is the consent step, taken through the real Approve handler: the
	// assembly's AdminDeps are what let it accept a v2 manifest.
	if rec := f.do(http.MethodPost, "/plugins/"+pluginID+"/approve", ""); rec.Code != http.StatusOK {
		t.Fatalf("approve = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	approved, err := q.GetPluginByID(ctx, pluginID)
	if err != nil {
		t.Fatalf("GetPluginByID after approve: %v", err)
	}
	if approved.Status != string(model.PluginStatusActive) {
		t.Fatalf("approved plugin status = %q, want %q", approved.Status, model.PluginStatusActive)
	}
	return f
}

// adminRouter mounts the real admin plugin handlers on the real AdminDeps the
// assembly hands to main.go.
func (f *e2eFixture) adminRouter() http.Handler {
	d := f.a.AdminDeps()
	lifecycle := admin.NewInstanceLifecycle(admin.InstanceLifecycleDeps{
		Q: f.q, Store: f.deps.Store, Publisher: f.deps.Publisher,
		ProcMgr: d.ProcMgr, Trigger: d.Trigger, Inflight: d.Inflight, Evictor: d.Evictor,
		Unreg: d.Unregistrar, Provisioner: d.Provisioner,
	})
	handler := admin.NewPluginHandler(admin.PluginHandlerDeps{
		Q: f.q, Publisher: f.deps.Publisher, ProcessManager: d.ProcMgr, Lifecycle: lifecycle,
		Store: f.deps.Store, Provisioner: d.Provisioner, CapabilityHealth: d.CapabilityHealth,
		ApproveV2Manifests: d.ApproveV2Manifests,
	})
	r := chi.NewRouter()
	r.Post("/plugins/{id}/approve", handler.ApprovePlugin)
	r.Post("/plugins/{id}/accept-manifest", handler.AcceptManifest)
	r.Post("/plugins/{id}/instances", handler.CreateInstance)
	r.Delete("/plugins/{id}/instances/{iid}", handler.DeleteInstance)
	return r
}

func (f *e2eFixture) do(method, path string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// createInstance creates the instance through the admin handler and returns its ID.
func (f *e2eFixture) createInstance() string {
	f.t.Helper()
	rec := f.do(http.MethodPost, "/plugins/"+f.pluginID+"/instances", `{"instance_name":"`+e2eInstanceName+`"}`)
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("create instance = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Data.ID == "" {
		f.t.Fatalf("create instance response %q: id=%q err=%v", rec.Body.String(), resp.Data.ID, err)
	}
	return resp.Data.ID
}

// boot hands the assembly the netguard-wrapped operator listener, which runs
// the synchronous first pass.
func (f *e2eFixture) boot() {
	f.t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		f.t.Fatalf("listen: %v", err)
	}
	f.t.Cleanup(func() { _ = ln.Close() })
	guarded, err := netguard.Wrap(ln, netip.MustParsePrefix(e2ePool), nil)
	if err != nil {
		f.t.Fatalf("netguard.Wrap: %v", err)
	}
	if err := f.a.OperatorListenerReady(guarded); err != nil {
		f.t.Fatalf("OperatorListenerReady: %v", err)
	}
}

// passUntil advances the reconciler one full pass at a time until cond holds.
// Each pass is awaited on its observe-pass event, which fires last in a pass, so
// cond sees the world a whole pass produced. Bringing a fresh instance up takes
// several passes: network, self-attach, first generation, create, health gate,
// switch.
func (f *e2eFixture) passUntil(what string, cond func() bool) {
	f.t.Helper()
	rec := f.a.reconciler.(*reconciler.Reconciler)
	const maxPasses = 40
	for i := 0; i < maxPasses; i++ {
		if cond() {
			return
		}
		f.pub.drain()
		rec.Kick()
		f.pub.await(f.t, reconciler.EventObservePassCompleted)
	}
	if !cond() {
		f.t.Fatalf("%s did not hold after %d reconcile passes", what, maxPasses)
	}
}

func (f *e2eFixture) managedServer(instanceID string) (db.McpServer, bool) {
	f.t.Helper()
	srv, err := f.q.GetMCPServerByPluginInstance(context.Background(), &instanceID)
	if err != nil {
		return db.McpServer{}, false
	}
	return srv, true
}

func (f *e2eFixture) toolCount(serverID string) int {
	f.t.Helper()
	tools, err := f.q.ListMCPToolsByServer(context.Background(), serverID)
	if err != nil {
		f.t.Fatalf("ListMCPToolsByServer: %v", err)
	}
	return len(tools)
}

// converged is "the managed endpoint exists, its tools were discovered, and
// both listeners are bound".
func (f *e2eFixture) converged(instanceID string) func() bool {
	return func() bool {
		srv, ok := f.managedServer(instanceID)
		if !ok || f.toolCount(srv.ID) == 0 {
			return false
		}
		return f.hostAddr(instanceID) != "" &&
			f.egressAddr(instanceID) != ""
	}
}

func e2ePolicyYAML(t *testing.T) string {
	return subscribedPolicyYAMLFor(t, e2eInstanceName, e2eEventKind)
}

func TestBootV2_EndToEnd(t *testing.T) {
	ctx := context.Background()
	f := newE2EFixture(t)

	instanceID := f.createInstance()
	row, err := f.q.GetPluginContainerByInstance(ctx, instanceID)
	if err != nil {
		t.Fatalf("the admin create wrote no plugin_containers desired state: %v", err)
	}
	if row.DesiredState != reconciler.DesiredRunning {
		t.Fatalf("desired_state = %q, want %q", row.DesiredState, reconciler.DesiredRunning)
	}

	testutil.InsertPolicy(t, f.deps.Store, "pol-1", "on-message", "subscribed", e2ePolicyYAML(t))
	launcher := &recordingLauncher{launched: make(chan runpkg.LaunchParams, 4)}
	f.a.bind(launcher)

	// Registered after the store, so it runs before the store closes; the
	// assembly's own cleanup (registered in newE2EFixture, so it runs later)
	// joins the events stream.
	mgr := runpkg.NewRunManager()
	t.Cleanup(mgr.Wait)

	f.boot()
	f.passUntil("the managed endpoint, its tools and both listeners", f.converged(instanceID))

	// Reconcile, attach, observe: the endpoint is the container's address and
	// the manifest's port, and the tools were discovered through it.
	srv, _ := f.managedServer(instanceID)
	if !mcp.IsManaged(srv) || srv.Name != e2eInstanceName {
		t.Errorf("managed row = %+v, want a managed row named %q", srv, e2eInstanceName)
	}
	if want := "http://" + f.pluginAddr + "/"; srv.Url != want {
		t.Errorf("managed url = %q, want %q", srv.Url, want)
	}
	if owner, ok := f.arbiter.Lookup(e2eInstanceName + ".echo"); !ok || owner.Name != e2eInstanceName {
		t.Errorf("tool reservation = %+v, %v; want owned by %q", owner, ok, e2eInstanceName)
	}

	// The fake plugin calls the host endpoint with the token its container was
	// created with: first-boot token, per-generation resolver, listener bound
	// to Gleipnir's reserved address.
	hostURL := f.rt.envOf(instanceID, hostclient.HostEndpointURLEnvVar)
	token := f.rt.envOf(instanceID, hostclient.InstanceTokenEnvVar)
	if hostURL == "" || token == "" {
		t.Fatalf("container env has host endpoint %q and token %q; want both", hostURL, token)
	}
	if !strings.HasPrefix(hostURL, "http://127.83.") || !strings.Contains(hostURL, ".2:") {
		t.Errorf("host endpoint %q is not at Gleipnir's reserved .2 address", hostURL)
	}
	client, err := hostclient.New(hostclient.WithBaseURL(hostURL), hostclient.WithToken(token))
	if err != nil {
		t.Fatalf("hostclient.New: %v", err)
	}
	cfg, err := client.GetInstanceConfig(ctx)
	if err != nil {
		t.Fatalf("host/get_instance_config with the first-boot token: %v", err)
	}
	if cfg.ConfigJSON != "{}" {
		t.Errorf("config_json = %q, want %q", cfg.ConfigJSON, "{}")
	}
	badClient, err := hostclient.New(hostclient.WithBaseURL(hostURL), hostclient.WithToken("not-the-token"))
	if err != nil {
		t.Fatalf("hostclient.New: %v", err)
	}
	if _, err := badClient.GetInstanceConfig(ctx); err == nil {
		t.Error("host endpoint accepted a token that was never issued")
	}

	// A managed tool call through the launcher's resolver: the classifier
	// vouches for the grant and the MCP registry resolves it.
	resolver := runpkg.NewDefaultToolResolver(f.deps.MCPRegistry, f.a.LauncherDeps().ToolClassifier, nil)
	resolved, err := resolver.ResolveCapabilities(ctx, []model.ToolCapability{{Tool: e2eInstanceName + ".echo"}})
	if err != nil {
		t.Fatalf("ResolveCapabilities: %v", err)
	}
	if len(resolved.MCPTools) != 1 {
		t.Fatalf("resolved %d MCP tools, want 1", len(resolved.MCPTools))
	}
	tool := resolved.MCPTools[0]
	if _, err := tool.Client.CallTool(ctx, tool.ToolName, map[string]any{}, mcp.CallOptions{}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	calls := f.plugin.tools.RequestsFor("tools/call")
	if len(calls) != 1 || calls[0].NameHeader != "echo" {
		t.Errorf("plugin saw tools/call %+v, want exactly one for echo", calls)
	}
	if violations := f.plugin.tools.Violations(); len(violations) != 0 {
		t.Errorf("the managed client broke the transport rules: %v", violations)
	}

	// The plugin's event reached the trigger pipeline and launched the policy.
	select {
	case params := <-launcher.launched:
		if params.PolicyID != "pol-1" || params.TriggerType != model.TriggerTypeSubscribed {
			t.Errorf("launch = policy %q trigger %q, want pol-1 subscribed", params.PolicyID, params.TriggerType)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the plugin's event never launched a run")
	}

	// Clean shutdown, in the order main.go drives it: quiesce, drain, shutdown.
	hostAddr := f.hostAddr(instanceID)
	f.a.Quiesce()
	mgr.CancelAll()
	mgr.Wait()
	f.a.Shutdown()

	if conn, err := net.DialTimeout("tcp", hostAddr, time.Second); err == nil {
		_ = conn.Close()
		t.Errorf("host endpoint listener at %s is still accepting after Shutdown", hostAddr)
	}
	if !f.rt.Closed() {
		t.Error("the container runtime connection was not closed")
	}
	// Containers are left running: desired state is durable and the next boot
	// converges on it.
	live, err := f.rt.ListByLabel(ctx, reconciler.LabelManaged, reconciler.ManagedValue)
	if err != nil {
		t.Fatalf("ListByLabel: %v", err)
	}
	if len(live) != 1 || live[0].State != container.ContainerStateRunning {
		t.Errorf("containers after shutdown = %+v, want the plugin's, still running", live)
	}
}
