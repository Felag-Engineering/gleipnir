//go:build dood && substratev2

package dood_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/moby/moby/api/pkg/stdcopy"
	dockerclient "github.com/moby/moby/client"

	"github.com/felag-engineering/gleipnir/internal/admin"
	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/infra/config"
	"github.com/felag-engineering/gleipnir/internal/infra/netguard"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/assembly"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

const (
	// bundleDirEnv names the directory holding conformance-stub-<mode>.tgz
	// (make conformance-stub-bundle). scripts/run-dood-suite.sh mounts it into
	// the runner container and sets this.
	bundleDirEnv = "DOOD_BUNDLE_DIR"

	// probeImageEnv overrides the image isolation probes run. It must already
	// be present locally: an instance network is Internal, so a container on
	// one cannot pull anything. The default is the runner container's own
	// image, which is present by construction.
	probeImageEnv = "DOOD_PROBE_IMAGE"

	// subnetPoolEnv overrides the pool the assembly carves instance networks
	// from. The default is random per run, see pickPool.
	subnetPoolEnv = "DOOD_SUBNET_POOL"
)

// labelRun tags the probe containers this suite creates so cleanup finds them
// by label even after a panic. The reconciler's own containers and networks
// carry the reconciler's labels instead, and are swept by instance and by pool.
const labelRun = "gleipnir.dood-test-run"

// convergeBudget bounds how long the suite waits at any one step. Generous
// relative to a local run because a flaky integration test is worse than a
// slow one; the whole suite is bounded by the job's own timeout.
const convergeBudget = 2 * time.Minute

// reconcileInterval is the pass cadence the assembly runs at in this suite. The
// default (30s) would put a fresh instance, which needs several passes, minutes
// from running; the suite synchronizes on the pass event, never on a sleep.
const reconcileInterval = time.Second

// harness is the whole runtime under test: one assembly.New over the real
// socket, plus the handles a test needs to drive and observe it.
type harness struct {
	t          *testing.T
	runID      string
	socketPath string
	selfID     container.ContainerID
	pool       netip.Prefix

	// rt is the suite's own connection to the socket, used for probes,
	// inspection and cleanup. The runtime under test is the assembly's.
	rt container.Runtime

	store   *db.Store
	q       *db.Queries
	arbiter *toolregistry.Registry
	mcpReg  *mcp.Registry
	asm     *assembly.Assembly
	passes  *passPublisher
	admin   http.Handler

	mu        sync.Mutex
	instances []string
}

// passPublisher is the event.Publisher the assembly publishes to. It turns the
// reconciler's per-pass event into a wake-up for WaitConverged, so the suite
// re-evaluates exactly when the world could have changed.
type passPublisher struct {
	passes chan struct{}
}

func (p *passPublisher) Publish(eventType string, _ json.RawMessage) {
	if eventType != reconciler.EventPassCompleted {
		return
	}
	// A buffer of one coalesces bursts: a waiter only needs to know "at least
	// one pass finished since I last looked".
	select {
	case p.passes <- struct{}{}:
	default:
	}
}

// newHarness resolves the real socket, boots the assembly against it and hands
// the operator-listener proof over, which runs the synchronous first pass.
//
// The socket is resolved through the production DetectPosture rather than a
// hardcoded path, so the suite also proves that detection finds what the runner
// provisioned. A missing socket or a missing container is t.Fatal, not a skip:
// the build tags already expressed the intent to run against a daemon, and a
// skip would read as a pass in every log that only counts failures.
func newHarness(t *testing.T) *harness {
	t.Helper()

	result, err := container.DetectPosture(container.LoadDetectConfig())
	if err != nil {
		t.Fatalf("resolving container posture: %v\n"+
			"this suite requires a real runtime socket; run it through scripts/run-dood-suite.sh", err)
	}
	if result.Posture == container.PostureManual {
		t.Fatal("posture resolved to manual: this suite writes to the socket and cannot run read-only")
	}
	t.Logf("dood posture: %s (socket %s)", result.Posture, result.SocketPath)

	rt, err := container.NewDockerRuntime(result.SocketPath)
	if err != nil {
		t.Fatalf("dialing %s: %v", result.SocketPath, err)
	}

	h := &harness{
		t:          t,
		runID:      fmt.Sprintf("t%d", time.Now().UnixNano()%1e9),
		socketPath: result.SocketPath,
		rt:         rt,
		pool:       pickPool(t),
		passes:     &passPublisher{passes: make(chan struct{}, 1)},
		arbiter:    toolregistry.New(),
	}

	// Cleanup is registered before anything is created. It runs last (LIFO):
	// after the assembly has stopped, so the reconciler cannot recreate what is
	// being removed, and after the store has closed, which is why it works from
	// an in-memory list of instance IDs instead of reading the database.
	t.Cleanup(h.cleanup)

	// The suite is only meaningful inside the container the assembly will
	// self-attach. The assembly resolves its own container ID through the same
	// function; asking first turns "no self-attach happened" (which New treats as
	// not containerized) into a loud failure here.
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()
	h.selfID, err = container.ResolveSelfContainerID(ctx, rt)
	if err != nil {
		t.Fatalf("resolving this process's own container: %v", err)
	}
	if h.selfID == "" {
		t.Fatal("this process is not running inside a container the runtime can see, so the assembly " +
			"cannot self-attach and no instance gateway would be reachable; run it through scripts/run-dood-suite.sh")
	}
	t.Logf("dood self container: %s", h.selfID)

	// The assembly's Shutdown is registered below, after the store, so it runs
	// first (cleanup is LIFO) and nothing writes to a closed store.
	h.store = testutil.NewTestStore(t)
	h.q = h.store.Queries()
	h.mcpReg = mcp.NewRegistry(h.q, mcp.WithToolNamespaceArbiter(h.arbiter))

	deps := assembly.Deps{
		Config: config.Config{
			PluginSubnetPool:         h.pool.String(),
			PluginsDir:               t.TempDir(),
			OAuthRefreshInterval:     time.Hour,
			OAuthRefreshLead:         time.Hour,
			PluginDedupSweepInterval: time.Hour,
		},
		Store:       h.store,
		Publisher:   h.passes,
		Arbiter:     h.arbiter,
		MCPRegistry: h.mcpReg,
	}
	h.asm, err = assembly.New(context.Background(), deps, assembly.WithReconcileInterval(reconcileInterval))
	if err != nil {
		t.Fatalf("assembly.New: %v", err)
	}
	t.Cleanup(h.asm.Shutdown)

	h.admin = h.adminRouter()

	// The operator API listener, wrapped by netguard: the proof the assembly
	// demands before it will self-attach to anything. Nothing serves on it; it
	// exists so the gate is the production one rather than bypassed.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for the operator API stand-in: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	guarded, err := netguard.Wrap(ln, h.pool, nil)
	if err != nil {
		t.Fatalf("netguard.Wrap: %v", err)
	}
	if err := h.asm.OperatorListenerReady(guarded); err != nil {
		t.Fatalf("OperatorListenerReady: %v", err)
	}
	return h
}

// pickPool returns the base pool for this run. A random /22 in 10.150-249/16
// space keeps two concurrent runs, and the leftovers of a killed one, from
// fighting over address space; it is outside the Gleipnir default (10.83/16),
// the substrate suite's pool and the daemons' own default pools.
func pickPool(t *testing.T) netip.Prefix {
	t.Helper()
	if v := os.Getenv(subnetPoolEnv); v != "" {
		pool, err := netip.ParsePrefix(v)
		if err != nil {
			t.Fatalf("%s=%q: %v", subnetPoolEnv, v, err)
		}
		return pool
	}
	second := 150 + rand.IntN(100)
	return netip.MustParsePrefix(fmt.Sprintf("10.%d.0.0/22", second))
}

// adminRouter mounts the real admin plugin handlers on the real AdminDeps the
// assembly hands to main.go. Approval and instance creation go through them
// because they are the operator's actual path; the suite builds no substrate
// component of its own.
func (h *harness) adminRouter() http.Handler {
	d := h.asm.AdminDeps()
	lifecycle := admin.NewInstanceLifecycle(admin.InstanceLifecycleDeps{
		Q: h.q, Store: h.store, Publisher: h.passes,
		ProcMgr: d.ProcMgr, Trigger: d.Trigger, Inflight: d.Inflight, Evictor: d.Evictor,
		Unreg: d.Unregistrar, Provisioner: d.Provisioner,
	})
	handler := admin.NewPluginHandler(admin.PluginHandlerDeps{
		Q: h.q, Publisher: h.passes, ProcessManager: d.ProcMgr, Lifecycle: lifecycle,
		Store: h.store, Provisioner: d.Provisioner, CapabilityHealth: d.CapabilityHealth,
		ApproveV2Manifests: d.ApproveV2Manifests,
	})
	r := chi.NewRouter()
	r.Post("/plugins/{id}/approve", handler.ApprovePlugin)
	r.Post("/plugins/{id}/instances", handler.CreateInstance)
	return r
}

func (h *harness) post(path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.admin.ServeHTTP(rec, req)
	return rec
}

// InstallBundle installs conformance-stub-<mode>.tgz through the OCI installer
// the upload route uses, then approves it through the admin Approve handler:
// an install lands in pending_review and no instance runs until an operator
// consents. It returns the plugin ID. Installing the same bundle again is a
// no-op that returns the same ID.
func (h *harness) InstallBundle(mode string) string {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()

	path := filepath.Join(bundleDir(), "conformance-stub-"+mode+".tgz")
	if _, err := os.Stat(path); err != nil {
		h.t.Fatalf("bundle missing: %v\nbuild it with `make conformance-stub-bundle` (see scripts/run-dood-suite.sh)", err)
	}
	pluginID, err := h.asm.AdminDeps().Installer.Install(ctx, path)
	if err != nil {
		h.t.Fatalf("installing %s: %v", path, err)
	}
	plugin, err := h.q.GetPluginByID(ctx, pluginID)
	if err != nil {
		h.t.Fatalf("GetPluginByID: %v", err)
	}
	if plugin.Status == string(model.PluginStatusActive) {
		return pluginID
	}
	if plugin.Status != string(model.PluginStatusPendingReview) {
		h.t.Fatalf("installed plugin status = %q, want %q", plugin.Status, model.PluginStatusPendingReview)
	}
	if rec := h.post("/plugins/"+pluginID+"/approve", ""); rec.Code != http.StatusOK {
		h.t.Fatalf("approve = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	return pluginID
}

func bundleDir() string {
	if dir := os.Getenv(bundleDirEnv); dir != "" {
		return dir
	}
	return filepath.Join("..", "..", "..", "dist")
}

// CreateInstance creates a named instance of an approved plugin, which writes
// its desired state and wakes the reconciler, and returns its ID. The ID is
// remembered at once so cleanup finds everything the instance grows.
func (h *harness) CreateInstance(pluginID, name string) string {
	h.t.Helper()
	rec := h.post("/plugins/"+pluginID+"/instances", `{"instance_name":"`+name+`"}`)
	if rec.Code != http.StatusCreated {
		h.t.Fatalf("create instance %q = %d, want 201: %s", name, rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Data.ID == "" {
		h.t.Fatalf("create instance response %q: id=%q err=%v", rec.Body.String(), resp.Data.ID, err)
	}
	h.mu.Lock()
	h.instances = append(h.instances, resp.Data.ID)
	h.mu.Unlock()
	return resp.Data.ID
}

// WaitConverged blocks until every instance has a managed endpoint row, has had
// its tools discovered, and has a running container with an address. It is
// signalled by the reconciler's pass event and re-evaluates after each one, so
// it waits for the system rather than polling on a timer.
func (h *harness) WaitConverged(instanceIDs ...string) {
	h.t.Helper()
	deadline := time.After(convergeBudget)
	var missing string
	for {
		missing = ""
		for _, id := range instanceIDs {
			if why := h.notConverged(id); why != "" {
				missing = fmt.Sprintf("instance %s: %s", id, why)
				break
			}
		}
		if missing == "" {
			return
		}
		select {
		case <-h.passes.passes:
		case <-deadline:
			h.t.Fatalf("did not converge within %s; last state: %s", convergeBudget, missing)
		}
	}
}

// notConverged returns why an instance is not up yet, or "" when it is.
func (h *harness) notConverged(instanceID string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	srv, err := h.q.GetMCPServerByPluginInstance(ctx, &instanceID)
	if err != nil {
		return "no managed endpoint row yet"
	}
	tools, err := h.q.ListMCPToolsByServer(ctx, srv.ID)
	if err != nil {
		return fmt.Sprintf("listing tools: %v", err)
	}
	if len(tools) == 0 {
		return "no tools discovered yet"
	}
	if _, ok := h.runningContainer(ctx, instanceID); !ok {
		return "no running container with an address yet"
	}
	return ""
}

// runningContainer returns the instance's running container, inspected so that
// it carries its address.
func (h *harness) runningContainer(ctx context.Context, instanceID string) (container.ContainerInfo, bool) {
	list, err := h.rt.ListByLabel(ctx, reconciler.LabelInstance, instanceID)
	if err != nil {
		return container.ContainerInfo{}, false
	}
	for _, c := range list {
		info, err := h.rt.Inspect(ctx, c.ID)
		if err == nil && info.State == container.ContainerStateRunning && info.IPAddress != "" {
			return info, true
		}
	}
	return container.ContainerInfo{}, false
}

// ManagedServer returns the instance's managed mcp_servers row.
func (h *harness) ManagedServer(instanceID string) (db.McpServer, error) {
	return h.q.GetMCPServerByPluginInstance(context.Background(), &instanceID)
}

// Client returns the MCP client for an instance's managed endpoint, resolved
// through the one registry the whole process shares, as a run would.
func (h *harness) Client(instanceID string) *mcp.Client {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()
	srv, err := h.ManagedServer(instanceID)
	if err != nil {
		h.t.Fatalf("no managed endpoint for instance %s: %v", instanceID, err)
	}
	client, err := h.mcpReg.ClientForServerID(ctx, srv.ID)
	if err != nil {
		h.t.Fatalf("resolving the MCP client for instance %s: %v", instanceID, err)
	}
	return client
}

// Topology is where an instance sits on the runtime's networks.
type topology struct {
	Network container.NetworkInfo
	Subnet  netip.Prefix
	// Plugin is the plugin container's address on its instance network.
	Plugin netip.Addr
	// Host is the address Gleipnir holds on that network (.2), where the host
	// endpoint listener binds.
	Host netip.Addr
}

// Topology reads an instance's network back from the daemon, so the addresses
// probes dial are what the daemon reports, not what the suite assumes.
func (h *harness) Topology(instanceID string) topology {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()

	nets, err := h.rt.ListNetworksByLabel(ctx, reconciler.LabelInstance, instanceID)
	if err != nil || len(nets) != 1 {
		h.t.Fatalf("networks of instance %s = %v (err %v), want exactly one", instanceID, nets, err)
	}
	subnet, err := netip.ParsePrefix(nets[0].Subnet)
	if err != nil {
		h.t.Fatalf("network %s subnet %q: %v", nets[0].Name, nets[0].Subnet, err)
	}
	info, ok := h.runningContainer(ctx, instanceID)
	if !ok {
		h.t.Fatalf("instance %s has no running container", instanceID)
	}
	plugin, err := netip.ParseAddr(info.IPAddress)
	if err != nil {
		h.t.Fatalf("container address %q: %v", info.IPAddress, err)
	}
	// The second usable address of the subnet is Gleipnir's by construction
	// (egress.GleipnirAddrOf); read back from the daemon's own inspection of
	// this container instead, so a drift between the two shows up here.
	self, err := h.rt.Inspect(ctx, h.selfID)
	if err != nil {
		h.t.Fatalf("inspecting the self container: %v", err)
	}
	host, err := netip.ParseAddr(self.NetworkAddresses[nets[0].Name])
	if err != nil {
		h.t.Fatalf("self address on %s = %q: %v (was the runner self-attached?)",
			nets[0].Name, self.NetworkAddresses[nets[0].Name], err)
	}
	return topology{Network: nets[0], Subnet: subnet, Plugin: plugin, Host: host}
}

// probeImage is what isolation probes run: an image that is already local and
// has a shell and nc. By default that is the runner container's own image.
func (h *harness) probeImage() string {
	h.t.Helper()
	if v := os.Getenv(probeImageEnv); v != "" {
		return v
	}
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()
	self, err := h.rt.Inspect(ctx, h.selfID)
	if err != nil {
		h.t.Fatalf("inspecting the self container for its image: %v", err)
	}
	return self.Image
}

// Dial reports whether a fresh container attached ONLY to network can open a TCP
// connection to addr. It is a container rather than a dial from this process
// because this process is itself attached to every instance network, so it
// would prove nothing about what a plugin can reach from inside its own.
//
// A probe that could not run at all (no nc, no shell) is a test failure rather
// than "not reached": otherwise a broken probe would read as isolation holding.
func (h *harness) Dial(name, network string, addr netip.AddrPort) (reached bool, output string) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()

	script := fmt.Sprintf("nc -w 3 -z %s %d; echo exit=$?", addr.Addr(), addr.Port())
	id, err := h.rt.Create(ctx, container.CreateOptions{
		Name:    "gleipnir-dood-" + h.runID + "-" + name,
		Image:   h.probeImage(),
		Network: network,
		Labels:  map[string]string{labelRun: h.runID},
		Command: []string{"sh", "-c", script},
		// Self-constraint requires these on every create. Setting them here is
		// what makes a probe failure mean the dial failed, not the create.
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges"},
		Sysctls:     container.RequiredSysctls(),
	})
	if err != nil {
		h.t.Fatalf("creating probe %s: %v", name, err)
	}
	if err := h.rt.Start(ctx, id); err != nil {
		h.t.Fatalf("starting probe %s: %v", name, err)
	}

	// The runtime interface offers no wait-for-exit, so this polls Inspect; the
	// bound is far above the probe's own 3s dial timeout.
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := h.rt.Inspect(ctx, id)
		if err != nil {
			h.t.Fatalf("inspecting probe %s: %v", name, err)
		}
		if info.State == container.ContainerStateExited {
			break
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("probe %s did not exit within its budget", name)
		}
		time.Sleep(100 * time.Millisecond)
	}

	output = h.logsOf(ctx, id)
	switch {
	case strings.Contains(output, "exit=0"):
		return true, output
	case strings.Contains(output, "exit=1"):
		return false, output
	default:
		h.t.Fatalf("probe %s did not run nc to a verdict (a missing nc or shell would otherwise read as isolation): %q",
			name, strings.TrimSpace(output))
		return false, output
	}
}

func (h *harness) logsOf(ctx context.Context, id container.ContainerID) string {
	rc, err := h.rt.Logs(ctx, id, container.LogOptions{})
	if err != nil {
		h.t.Fatalf("reading logs of %s: %v", id, err)
	}
	defer rc.Close()

	// No TTY, so stdout and stderr arrive multiplexed with 8-byte frame headers.
	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, io.LimitReader(rc, 8<<10)); err != nil {
		h.t.Fatalf("demultiplexing logs of %s: %v", id, err)
	}
	return out.String()
}

// cleanup removes everything the run created, by label and by the reconciler's
// own labels, so a failed or panicking test leaves nothing on the runner.
//
// force=true on removal, unlike the reconciler's deliberate force=false: the
// reconciler refuses to destroy evidence mid-convergence, while cleanup exists
// to leave nothing behind.
func (h *harness) cleanup() {
	t := h.t
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()

	probes, err := h.rt.ListByLabel(ctx, labelRun, h.runID)
	if err != nil {
		t.Errorf("cleanup: listing probes: %v", err)
	}
	h.mu.Lock()
	instances := slices.Clone(h.instances)
	h.mu.Unlock()
	containers := probes
	for _, id := range instances {
		list, err := h.rt.ListByLabel(ctx, reconciler.LabelInstance, id)
		if err != nil {
			t.Errorf("cleanup: listing containers of %s: %v", id, err)
		}
		containers = append(containers, list...)
	}
	for _, c := range containers {
		if err := h.rt.Remove(ctx, c.ID, true); err != nil {
			t.Errorf("cleanup: removing container %s: %v", c.Name, err)
		}
	}

	h.removeNetworks(ctx)
	h.removeVolumes(ctx, instances)

	if err := h.rt.Close(); err != nil {
		t.Errorf("cleanup: closing runtime: %v", err)
	}
}

// removeNetworks removes the instance networks in this run's pool. Selecting by
// pool rather than by a list of IDs also catches a network whose instance never
// got far enough to be recorded. The self container is detached first: a
// network with an active endpoint cannot be removed.
func (h *harness) removeNetworks(ctx context.Context) {
	t := h.t
	nets, err := h.rt.ListNetworksByLabel(ctx, reconciler.LabelManaged, reconciler.ManagedValue)
	if err != nil {
		t.Errorf("cleanup: listing networks: %v", err)
		return
	}
	var self container.ContainerInfo
	if h.selfID != "" {
		self, err = h.rt.Inspect(ctx, h.selfID)
		if err != nil {
			t.Errorf("cleanup: inspecting the self container: %v", err)
		}
	}
	for _, n := range nets {
		subnet, err := netip.ParsePrefix(n.Subnet)
		if err != nil || !h.pool.Contains(subnet.Addr()) {
			continue
		}
		if slices.Contains(self.Networks, n.ID) || slices.Contains(self.NetworkNames, n.Name) {
			if err := h.rt.DisconnectNetwork(ctx, n.ID, h.selfID); err != nil {
				t.Errorf("cleanup: detaching from network %s: %v", n.Name, err)
			}
		}
		if err := h.rt.RemoveNetwork(ctx, n.ID); err != nil {
			t.Errorf("cleanup: removing network %s: %v", n.Name, err)
		}
	}
}

// removeVolumes removes the per-instance data volumes. The container interface
// has no volume removal because production never deletes one on its own, so
// the suite talks to the socket directly here.
func (h *harness) removeVolumes(ctx context.Context, instances []string) {
	t := h.t
	if len(instances) == 0 {
		return
	}
	cli, err := dockerclient.New(dockerclient.WithHost("unix://" + h.socketPath))
	if err != nil {
		t.Errorf("cleanup: dialing %s to remove volumes: %v", h.socketPath, err)
		return
	}
	defer cli.Close()
	for _, id := range instances {
		name := "gleipnir-plugin-" + id + "-data"
		if _, err := cli.VolumeRemove(ctx, name, dockerclient.VolumeRemoveOptions{Force: true}); err != nil {
			t.Errorf("cleanup: removing volume %s: %v", name, err)
		}
	}
}
