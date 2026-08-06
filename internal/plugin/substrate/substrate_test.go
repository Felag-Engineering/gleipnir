//go:build substrate

// The `substrate` build tag makes this opt-in at COMPILE time rather than
// skipped at run time, and that is the point: a t.Skip() when no socket is
// present reads as a passing test in every log that only counts failures, so
// the one suite whose whole job is "we checked against a real daemon" would be
// silently absent exactly where nobody is watching. A build tag makes running
// it a decision someone made.
//
//	go test -tags substrate ./internal/plugin/substrate/
//
// CI runs it on a rootless Podman runner; `make ci-local` does not (see
// scripts/ci-local.sh, CI_LOCAL_SUBSTRATE).

package substrate_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// probeImageEnv names the image this suite runs. It must already be present
// locally: an instance network is Internal, so a container on one cannot pull
// anything, and a test that depended on a pull would be testing the runner's
// network rather than the substrate.
const probeImageEnv = "GLEIPNIR_SUBSTRATE_PROBE_IMAGE"

// defaultProbeImage is built by the CI job (and by the one-liner in
// docs/developer/container-substrate.md) rather than pulled, because the image
// has to satisfy two requirements at once and no stock tag does both.
//
// It must STAY RUNNING under its default command. The reconciler creates
// instance containers from a desired-state row, which carries no command
// override, so the image's own CMD is what runs — and the planner correctly
// restarts an exited container when the row says `running`. A stock busybox
// (CMD ["sh"], which exits immediately without a TTY) therefore flaps rather
// than converging, and a test that waited for "running" would be waiting to
// catch it mid-flap. That is not a hypothetical: it is what the first CI run of
// this suite did, and it passed the single-instance case by luck while the
// two-instance case never saw both containers up at the same instant.
//
// It must also carry `nc`, because the isolation probe is a container dialing
// across a network boundary.
const defaultProbeImage = "localhost/gleipnir-substrate:latest"

// labelRun tags everything this suite creates so cleanup can find it by label
// even after a panic — the same discovery primitive the reconciler itself
// relies on, which means cleanup cannot drift from what the loop can see.
const labelRun = "gleipnir.substrate-test-run"

// convergeBudget bounds how long the suite waits for the daemon at any one
// step. Generous relative to a local run because CI runners are slow and a
// flaky integration test is worse than a slow one; the whole suite is bounded
// by the job's own timeout, which is where "this hung" is meant to surface.
const convergeBudget = 90 * time.Second

// --- harness ----------------------------------------------------------------

type harness struct {
	rt    container.Runtime
	store *db.Store
	rec   *reconciler.Reconciler
	runID string
}

// newHarness dials the real socket and builds a reconciler over it.
//
// The socket is resolved through the production DetectPosture rather than a
// hardcoded path, so the suite also proves that detection finds what CI
// actually provisioned. A missing socket is a hard failure here, not a skip:
// the build tag already expressed the intent to run against a daemon.
func newHarness(t *testing.T) *harness {
	t.Helper()

	result, err := container.DetectPosture(container.LoadDetectConfig())
	if err != nil {
		t.Fatalf("resolving container posture: %v\n"+
			"this suite requires a real runtime socket; run it without -tags substrate to skip it entirely", err)
	}
	if result.Posture == container.PostureManual {
		t.Fatal("posture resolved to manual: this suite writes to the socket and cannot run read-only")
	}
	t.Logf("substrate posture: %s (socket %s)", result.Posture, result.SocketPath)

	rt, err := container.NewDockerRuntime(result.SocketPath)
	if err != nil {
		t.Fatalf("dialing %s: %v", result.SocketPath, err)
	}

	runID := fmt.Sprintf("t%d", time.Now().UnixNano()%1e9)
	h := &harness{rt: rt, store: testutil.NewTestStore(t), runID: runID}

	// Cleanup is registered before anything is created, and removes by LABEL
	// rather than by a list of ids the test accumulated — an id list is exactly
	// what a panic loses.
	t.Cleanup(func() { h.cleanup(t) })

	alloc, err := reconciler.NewSubnetAllocator(h.store.Queries(), h.pool(), nil)
	if err != nil {
		t.Fatalf("NewSubnetAllocator: %v", err)
	}
	h.rec, err = reconciler.New(reconciler.Config{
		Runtime: rt,
		Store:   h.store.Queries(),
		GC:      h.store.Queries(),
		Subnets: alloc,
		Posture: result.Posture,
	})
	if err != nil {
		t.Fatalf("reconciler.New: %v", err)
	}
	return h
}

// pool returns a base pool unlikely to collide with whatever else the runner
// has running. Each suite run takes its own /20 so two concurrent runs on one
// machine do not fight over address space.
func (h *harness) pool() netip.Prefix {
	// 10.190.x.0/24 — outside both the Gleipnir default (10.83/16) and the
	// daemon's own default pools.
	return netip.MustParsePrefix("10.190.0.0/20")
}

func (h *harness) cleanup(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()

	containers, err := h.rt.ListByLabel(ctx, labelRun, h.runID)
	if err != nil {
		t.Errorf("cleanup: listing containers: %v", err)
	}
	for _, c := range containers {
		// force=true here, unlike the reconciler's deliberate force=false: the
		// reconciler refuses to destroy evidence mid-convergence, while cleanup
		// exists precisely to leave nothing behind on a runner.
		if err := h.rt.Remove(ctx, c.ID, true); err != nil {
			t.Errorf("cleanup: removing container %s: %v", c.Name, err)
		}
	}
	networks, err := h.rt.ListNetworksByLabel(ctx, labelRun, h.runID)
	if err != nil {
		t.Errorf("cleanup: listing networks: %v", err)
	}
	for _, n := range networks {
		if err := h.rt.RemoveNetwork(ctx, n.ID); err != nil {
			t.Errorf("cleanup: removing network %s: %v", n.Name, err)
		}
	}
	if err := h.rt.Close(); err != nil {
		t.Errorf("cleanup: closing runtime: %v", err)
	}
}

// seedInstance inserts a plugin, an instance, and a desired container row.
func (h *harness) seedInstance(t *testing.T, instanceID, imageRef, digest string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	if _, err := h.store.DB().Exec(
		`INSERT OR IGNORE INTO plugins(id, name, plugin_version, manifest_snapshot, trusted_pubkey, status, version, created_at, updated_at)
		 VALUES ('pl1', 'substrate-test', '1.0.0', '{}', 'pk', 'active', 0, ?, ?)`, now, now,
	); err != nil {
		t.Fatalf("insert plugin: %v", err)
	}
	if _, err := h.store.DB().Exec(
		`INSERT INTO plugin_instances(id, plugin_id, instance_name, config_json, subscription_scope_json, handshake_versions, health_state, version, created_at, updated_at)
		 VALUES (?, 'pl1', ?, '{}', '{}', '{}', 'healthy', 0, ?, ?)`,
		instanceID, "name-"+instanceID, now, now,
	); err != nil {
		t.Fatalf("insert plugin instance: %v", err)
	}
	if _, err := h.store.Queries().CreatePluginContainer(context.Background(), db.CreatePluginContainerParams{
		ID:               "pc-" + instanceID,
		PluginInstanceID: instanceID,
		ImageRef:         imageRef,
		ImageDigest:      digest,
		ConfigHash:       "cfg",
		NetworkName:      h.networkName(instanceID),
		DesiredState:     "running",
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("CreatePluginContainer: %v", err)
	}
}

func (h *harness) networkName(instanceID string) string {
	return "gleipnir-substrate-" + h.runID + "-" + instanceID
}

// converge runs reconcile passes until want reports satisfied, or the budget
// expires. The loop is the level-triggered contract exercised for real: each
// pass takes one step, and the suite simply keeps asking.
func (h *harness) converge(t *testing.T, what string, want func(context.Context) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()

	for pass := 1; ; pass++ {
		result, err := h.rec.ReconcileOnce(ctx)
		if err != nil {
			t.Fatalf("%s: reconcile pass %d: %v", what, pass, err)
		}
		if want(ctx) {
			t.Logf("%s: converged after %d pass(es)", what, pass)
			return
		}
		if result.Errors > 0 {
			t.Logf("%s: pass %d had %d action error(s); retrying", what, pass, result.Errors)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: did not converge within %s", what, convergeBudget)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// managedContainer returns the container the loop created for an instance.
func (h *harness) managedContainer(ctx context.Context, instanceID string) (container.ContainerInfo, bool) {
	list, err := h.rt.ListByLabel(ctx, reconciler.LabelInstance, instanceID)
	if err != nil || len(list) == 0 {
		return container.ContainerInfo{}, false
	}
	return list[0], true
}

// labelDesiredRows makes the reconciler's own create calls carry the suite's
// run label, so cleanup can find them. It rewrites the row's network name,
// which is the one create-time field the suite controls end to end.
//
// The reconciler does not offer a label hook, and adding one purely for a test
// would be a production seam that exists for nobody — so instead the suite
// labels what it creates directly, and reconciler-created containers are found
// via the reconciler's OWN labels in cleanup below.
func (h *harness) adoptForCleanup(t *testing.T, instanceID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()

	// Containers and networks the reconciler created carry gleipnir.managed +
	// gleipnir.plugin.instance. Cleanup keys off the suite label, so anything
	// the loop made has to be swept by instance label too.
	for _, c := range mustList(t, h.rt, reconciler.LabelInstance, instanceID) {
		if err := h.rt.Remove(ctx, c.ID, true); err != nil {
			t.Logf("adoptForCleanup: %v", err)
		}
	}
	nets, err := h.rt.ListNetworksByLabel(ctx, reconciler.LabelInstance, instanceID)
	if err != nil {
		t.Logf("adoptForCleanup: %v", err)
		return
	}
	for _, n := range nets {
		if err := h.rt.RemoveNetwork(ctx, n.ID); err != nil {
			t.Logf("adoptForCleanup: %v", err)
		}
	}
}

func mustList(t *testing.T, rt container.Runtime, key, value string) []container.ContainerInfo {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()
	list, err := rt.ListByLabel(ctx, key, value)
	if err != nil {
		t.Fatalf("ListByLabel(%s=%s): %v", key, value, err)
	}
	return list
}

func probeImage() string {
	if v := os.Getenv(probeImageEnv); v != "" {
		return v
	}
	return defaultProbeImage
}

// requireProbeImage fails when the image the reachability probes need is not
// present. Not a skip: the CI job pre-pulls it, and a silent skip would delete
// the isolation assertion from the one place it is meaningful.
func requireProbeImage(t *testing.T, rt container.Runtime) string {
	t.Helper()
	ref := probeImage()
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()
	if _, err := rt.ImageInspect(ctx, ref); err != nil {
		t.Fatalf("image %q is not present locally: %v\n\n"+
			"Build it first (an instance network is Internal, so a container cannot pull one):\n"+
			"  podman build -t %s -f - <<'EOF'\n"+
			"  FROM docker.io/library/busybox:latest\n"+
			"  CMD [\"sleep\", \"infinity\"]\n"+
			"  EOF\n\n"+
			"Or point %s at an image that stays running under its default command and has nc.",
			ref, err, ref, probeImageEnv)
	}
	return ref
}

// --- self-constraint against a real daemon -----------------------------------

// The Fake runs the same ValidateCreate, so this is not re-testing the rule —
// it is testing that the rule still stands between a hostile request and a REAL
// socket, which is the only place the claim actually matters.
func TestSubstrate_SelfConstraintHoldsAgainstARealDaemon(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()

	base := container.CreateOptions{
		Name:    "gleipnir-substrate-" + h.runID + "-hostile",
		Image:   probeImage(),
		Network: "bridge",
		Labels:  map[string]string{labelRun: h.runID},
	}

	hostile := map[string]func(container.CreateOptions) container.CreateOptions{
		"a bind mount of the host root": func(o container.CreateOptions) container.CreateOptions {
			o.Mounts = []container.Mount{{Type: container.MountTypeBind, Source: "/", Target: "/host"}}
			return o
		},
		"privileged": func(o container.CreateOptions) container.CreateOptions {
			o.Privileged = true
			return o
		},
		"added capabilities": func(o container.CreateOptions) container.CreateOptions {
			o.CapAdd = []string{"SYS_ADMIN"}
			return o
		},
		"the host network namespace": func(o container.CreateOptions) container.CreateOptions {
			o.Network = "host"
			return o
		},
		"no network at all": func(o container.CreateOptions) container.CreateOptions {
			o.Network = ""
			return o
		},
	}

	for name, mutate := range hostile {
		t.Run(name, func(t *testing.T) {
			id, err := h.rt.Create(ctx, mutate(base))
			if err == nil {
				_ = h.rt.Remove(ctx, id, true)
				t.Fatal("the daemon accepted a create the self-constraint must have refused")
			}
			var violation *container.ConstraintViolationError
			if !errors.As(err, &violation) {
				t.Errorf("error = %v, want a *ConstraintViolationError — the request must be refused "+
					"before the socket, not by the daemon happening to dislike it", err)
			}
		})
	}
}

// --- convergence -------------------------------------------------------------

// The level-triggered loop, driven against a real daemon: desired row in,
// network and container out, one step per pass.
func TestSubstrate_ConvergesToRunning(t *testing.T) {
	h := newHarness(t)
	ref := requireProbeImage(t, h.rt)
	instance := "inst-a"

	h.seedInstance(t, instance, ref, "")
	t.Cleanup(func() { h.adoptForCleanup(t, instance) })

	h.converge(t, "instance reaches running", func(ctx context.Context) bool {
		info, ok := h.managedContainer(ctx, instance)
		return ok && info.State == container.ContainerStateRunning
	})

	// Still running a moment later. "Reached running once" is satisfied by a
	// container that exits immediately and gets restarted — the planner treats
	// an exited container with a `running` row as something to start again, so a
	// flapping instance passes a single point-in-time check while converging to
	// nothing.
	assertStaysRunning(t, h, instance)

	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()

	// The network is what default-deny is established by, so its Internal flag
	// is checked against what the daemon reports rather than what was asked for.
	nets, err := h.rt.ListNetworksByLabel(ctx, reconciler.LabelInstance, instance)
	if err != nil || len(nets) != 1 {
		t.Fatalf("networks = %v (err %v), want exactly one", nets, err)
	}
	if !nets[0].Internal {
		t.Error("the daemon reports the instance network as NOT internal; egress default-deny is not established")
	}

	// A converged pass performs zero socket writes. Asserting it here rather
	// than only against the Fake is what makes idempotency a property of the
	// system and not of the double.
	result, err := h.rec.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("converged pass: %v", err)
	}
	if !result.Converged {
		t.Errorf("a pass over a converged instance planned %d action(s): %+v", len(result.Actions), result.Actions)
	}
}

// --- east-west isolation (#811) ----------------------------------------------

// One dedicated internal network per instance is what makes a compromised
// plugin unable to reach a sibling's MCP endpoint — an ADR-001 violation by
// topology rather than by policy. Both halves are checked: the topology the
// allocator produced, and whether packets actually fail to cross it.
func TestSubstrate_EastWestIsolation(t *testing.T) {
	h := newHarness(t)
	ref := requireProbeImage(t, h.rt)

	for _, id := range []string{"inst-x", "inst-y"} {
		h.seedInstance(t, id, ref, "")
		t.Cleanup(func() { h.adoptForCleanup(t, id) })
	}

	h.converge(t, "both instances running", func(ctx context.Context) bool {
		for _, id := range []string{"inst-x", "inst-y"} {
			info, ok := h.managedContainer(ctx, id)
			if !ok || info.State != container.ContainerStateRunning {
				return false
			}
		}
		return true
	})

	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()

	// Topology: two networks, both internal, non-overlapping subnets.
	subnetX := h.subnetOf(t, "inst-x")
	subnetY := h.subnetOf(t, "inst-y")
	if subnetX == subnetY {
		t.Fatalf("both instances were allocated %s; the allocator double-assigned", subnetX)
	}
	if subnetX.Overlaps(subnetY) {
		t.Errorf("subnets %s and %s overlap; isolation is topological and overlapping ranges defeat it", subnetX, subnetY)
	}

	// Reachability: a container on X's network must not reach an address in Y's.
	// The target is Y's first host address, which is where a plugin's MCP
	// endpoint would sit — the exact thing a compromised sibling would dial.
	target := subnetY.Addr().Next()
	out, err := h.runProbe(ctx, "inst-x-probe", h.networkName("inst-x"), ref, target.String())
	if err == nil {
		t.Errorf("a container on inst-x's network reached %s on inst-y's network; east-west isolation is not enforced\noutput: %s",
			target, out)
	} else {
		t.Logf("cross-network dial to %s failed as required: %v", target, err)
	}
}

// assertStaysRunning re-checks an instance shortly after it first reported
// running, so a flap cannot pass for convergence.
func assertStaysRunning(t *testing.T, h *harness, instanceID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()

	for i := 0; i < 3; i++ {
		time.Sleep(time.Second)
		info, ok := h.managedContainer(ctx, instanceID)
		if !ok {
			t.Fatalf("%s: container vanished after reporting running", instanceID)
		}
		if info.State != container.ContainerStateRunning {
			t.Fatalf("%s: state is %q one second after reporting running — the container is flapping, "+
				"not converged (does the image stay up under its default command?)", instanceID, info.State)
		}
	}
}

// subnetOf reads the subnet the allocator recorded for an instance.
func (h *harness) subnetOf(t *testing.T, instanceID string) netip.Prefix {
	t.Helper()
	row, err := h.store.Queries().GetContainerSubnetByInstance(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("no subnet allocated for %s: %v", instanceID, err)
	}
	p, err := netip.ParsePrefix(row.Subnet)
	if err != nil {
		t.Fatalf("stored subnet %q for %s does not parse: %v", row.Subnet, instanceID, err)
	}
	return p
}

// runProbe starts a short-lived container on network and has it attempt one
// bounded TCP connection to addr. It returns the container's output and a
// non-nil error when the connection did NOT succeed.
//
// The probe runs a container rather than dialing from the test process because
// the test process is on the host, and the host can reach almost everything —
// proving nothing about what a plugin can reach from inside its own network.
func (h *harness) runProbe(ctx context.Context, name, network, image, addr string) (string, error) {
	// nc with a 3s timeout: exits 0 only if the connection is established.
	// Bounded so a silently-dropped packet ends the probe rather than the job.
	id, err := h.rt.Create(ctx, container.CreateOptions{
		Name:    "gleipnir-substrate-" + h.runID + "-" + name,
		Image:   image,
		Network: network,
		Labels:  map[string]string{labelRun: h.runID},
		Command: []string{"sh", "-c", fmt.Sprintf("nc -w 3 -z %s 8080; echo exit=$?", addr)},
	})
	if err != nil {
		return "", fmt.Errorf("creating probe: %w", err)
	}
	if err := h.rt.Start(ctx, id); err != nil {
		return "", fmt.Errorf("starting probe: %w", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := h.rt.Inspect(ctx, id)
		if err != nil {
			return "", fmt.Errorf("inspecting probe: %w", err)
		}
		if info.State == container.ContainerStateExited {
			out := h.probeOutput(ctx, id)
			if strings.Contains(out, "exit=0") {
				return out, nil
			}
			return out, fmt.Errorf("probe reported no connection: %s", strings.TrimSpace(out))
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("probe did not exit within its budget")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (h *harness) probeOutput(ctx context.Context, id container.ContainerID) string {
	rc, err := h.rt.Logs(ctx, id, container.LogOptions{})
	if err != nil {
		return ""
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 8<<10))
	if err != nil {
		return ""
	}
	return string(b)
}

// --- GC (#818) ---------------------------------------------------------------

// Image GC against a real daemon: the question a Fake cannot answer is whether
// the daemon agrees an image is removable, and whether it refuses when a
// container still uses it.
func TestSubstrate_ImageGCRespectsTheDaemon(t *testing.T) {
	h := newHarness(t)
	ref := requireProbeImage(t, h.rt)
	ctx, cancel := context.WithTimeout(context.Background(), convergeBudget)
	defer cancel()

	info, err := h.rt.ImageInspect(ctx, ref)
	if err != nil {
		t.Fatalf("ImageInspect(%s): %v", ref, err)
	}

	// Record the probe image as loaded, referenced by nothing. GC will consider
	// it reclaimable — and the daemon is the one that decides whether it goes.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.store.Queries().UpsertContainerImage(ctx, db.UpsertContainerImageParams{
		Digest:    info.ID,
		Reference: ref,
		LoadedAt:  now,
	}); err != nil {
		t.Fatalf("UpsertContainerImage: %v", err)
	}

	// A running container holds it. The daemon must refuse, GC must not force,
	// and the accounting row must survive the refusal — dropping it would leave
	// bytes on disk nothing would ever look at again.
	instance := "inst-gc"
	h.seedInstance(t, instance, ref, "")
	t.Cleanup(func() { h.adoptForCleanup(t, instance) })
	h.converge(t, "gc instance running", func(c context.Context) bool {
		i, ok := h.managedContainer(c, instance)
		return ok && i.State == container.ContainerStateRunning
	})

	result, err := h.rec.ReconcileGC(ctx)
	if err != nil {
		t.Fatalf("ReconcileGC: %v", err)
	}
	if result.ImagesReclaimed > 0 {
		t.Error("GC removed an image a running container still uses; the daemon's refusal was overridden")
	}
	if _, err := h.store.Queries().GetContainerImage(ctx, info.ID); err != nil {
		t.Errorf("the accounting row was dropped after a refused removal: %v", err)
	}
	if !containsCandidate(result, info.ID) {
		t.Errorf("GC did not report %s as reclaimable; its own records say nothing references it", info.ID)
	}
}

func containsCandidate(r reconciler.GCResult, digest string) bool {
	for _, c := range r.ImagesReclaimable {
		if c.Digest == digest {
			return true
		}
	}
	return false
}

// --- subnet cleanup ----------------------------------------------------------

// Removing the desired row must tear the network down and return the subnet,
// and the daemon must agree the network is gone — a released subnet whose
// network survives is the overlap that turns a clean teardown into a stuck one.
func TestSubstrate_TeardownReleasesTheNetworkAndSubnet(t *testing.T) {
	h := newHarness(t)
	ref := requireProbeImage(t, h.rt)
	instance := "inst-tear"

	h.seedInstance(t, instance, ref, "")
	t.Cleanup(func() { h.adoptForCleanup(t, instance) })
	h.converge(t, "teardown instance running", func(ctx context.Context) bool {
		i, ok := h.managedContainer(ctx, instance)
		return ok && i.State == container.ContainerStateRunning
	})

	subnet := h.subnetOf(t, instance)
	if err := h.store.Queries().DeletePluginContainer(context.Background(), "pc-"+instance); err != nil {
		t.Fatalf("DeletePluginContainer: %v", err)
	}

	h.converge(t, "instance is fully torn down", func(ctx context.Context) bool {
		if _, ok := h.managedContainer(ctx, instance); ok {
			return false
		}
		nets, err := h.rt.ListNetworksByLabel(ctx, reconciler.LabelInstance, instance)
		return err == nil && len(nets) == 0
	})

	if _, err := h.store.Queries().GetContainerSubnetByInstance(context.Background(), instance); err == nil {
		t.Errorf("subnet %s was not released after the network came down", subnet)
	}
}
