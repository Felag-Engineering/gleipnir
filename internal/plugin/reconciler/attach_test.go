package reconciler

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/container"
)

func managedRow(instanceID, url string) db.McpServer {
	return db.McpServer{ID: "srv-" + instanceID, Name: "i-" + instanceID, Url: url, PluginInstanceID: &instanceID}
}

func TestPlanAttach(t *testing.T) {
	const (
		urlA = "http://10.83.4.2:8080/"
		urlB = "http://10.83.4.3:8080/"
	)
	serving := func(id, url string) attachInstance {
		return attachInstance{InstanceID: id, InstanceName: "i-" + id, URL: url}
	}

	tests := []struct {
		name      string
		instances []attachInstance
		rows      []db.McpServer
		want      []AttachAction
	}{
		{
			name:      "new active generation registers",
			instances: []attachInstance{serving("a", urlA)},
			want: []AttachAction{{
				Kind: AttachRegister, InstanceID: "a",
				Endpoint: mcp.ManagedEndpoint{InstanceID: "a", InstanceName: "i-a", URL: urlA},
			}},
		},
		{
			name:      "converged row is left alone",
			instances: []attachInstance{serving("a", urlA)},
			rows:      []db.McpServer{managedRow("a", urlA)},
			want:      []AttachAction{{Kind: AttachNone, InstanceID: "a"}},
		},
		{
			name:      "rotation switch repoints the existing row",
			instances: []attachInstance{serving("a", urlB)},
			rows:      []db.McpServer{managedRow("a", urlA)},
			want: []AttachAction{{
				Kind: AttachRegister, InstanceID: "a",
				Endpoint: mcp.ManagedEndpoint{InstanceID: "a", InstanceName: "i-a", URL: urlB},
			}},
		},
		{
			name: "no live generation deregisters",
			rows: []db.McpServer{managedRow("a", urlA)},
			want: []AttachAction{{Kind: AttachDeregister, InstanceID: "a"}},
		},
		{
			name:      "live but unresolvable instance keeps its row",
			instances: []attachInstance{{InstanceID: "a", InstanceName: "i-a", HoldReason: "inspecting container: boom"}},
			rows:      []db.McpServer{managedRow("a", urlA)},
			want:      []AttachAction{{Kind: AttachNone, InstanceID: "a"}},
		},
		{
			name:      "instances are planned independently and in order",
			instances: []attachInstance{serving("b", urlB), serving("a", urlA)},
			rows:      []db.McpServer{managedRow("c", urlA)},
			want: []AttachAction{
				{Kind: AttachRegister, InstanceID: "a"},
				{Kind: AttachRegister, InstanceID: "b"},
				{Kind: AttachDeregister, InstanceID: "c"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planAttach(tt.instances, tt.rows, nil)
			if len(got) != len(tt.want) {
				t.Fatalf("planAttach() = %+v, want %d actions", got, len(tt.want))
			}
			for i, want := range tt.want {
				if got[i].Kind != want.Kind || got[i].InstanceID != want.InstanceID {
					t.Errorf("action %d = {%s %s}, want {%s %s}", i, got[i].Kind, got[i].InstanceID, want.Kind, want.InstanceID)
				}
				if want.Endpoint.URL != "" && got[i].Endpoint != want.Endpoint {
					t.Errorf("action %d endpoint = %+v, want %+v", i, got[i].Endpoint, want.Endpoint)
				}
			}
		})
	}
}

func TestServingFromGenerations(t *testing.T) {
	cid := func(s string) *string { return &s }
	tests := []struct {
		name        string
		generations []db.PluginContainerGeneration
		wantID      container.ContainerID
		wantHold    bool
	}{
		{
			name: "active generation serves",
			generations: []db.PluginContainerGeneration{
				{PluginInstanceID: "a", Status: GenActive, ContainerID: cid("c1")},
			},
			wantID: "c1",
		},
		{
			name: "switch: new active wins over draining old",
			generations: []db.PluginContainerGeneration{
				{PluginInstanceID: "a", Generation: 1, Status: GenDraining, ContainerID: cid("old")},
				{PluginInstanceID: "a", Generation: 2, Status: GenActive, ContainerID: cid("new")},
			},
			wantID: "new",
		},
		{
			name: "a generation still gating is not routable",
			generations: []db.PluginContainerGeneration{
				{PluginInstanceID: "a", Status: GenStarting, ContainerID: cid("c1")},
			},
			wantHold: true,
		},
		{
			name: "active row with no container is held",
			generations: []db.PluginContainerGeneration{
				{PluginInstanceID: "a", Status: GenActive},
			},
			wantHold: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := servingFromGenerations(tt.generations)
			if len(got) != 1 {
				t.Fatalf("servingFromGenerations() = %+v, want one instance", got)
			}
			if got[0].ContainerID != tt.wantID {
				t.Errorf("ContainerID = %q, want %q", got[0].ContainerID, tt.wantID)
			}
			if (got[0].HoldReason != "") != tt.wantHold {
				t.Errorf("HoldReason = %q, wantHold = %v", got[0].HoldReason, tt.wantHold)
			}
		})
	}
}

const attachManifest = `schema_version: "2"
name: test-plugin
version: "1.0.0"
package:
  registry_type: oci
  identifier: ghcr.io/acme/p@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  transport:
    type: streamable-http
    port: 8080
gleipnir:
  profiles:
    tool_provider: {}
`

type attachFixture struct {
	store *db.Store
	fake  *container.Fake
	rt    *countingRuntime
	r     *Reconciler
}

func newAttachFixture(t *testing.T, posture container.Posture) *attachFixture {
	t.Helper()

	store, err := db.Open(filepath.Join(t.TempDir(), "attach.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	fake := newTestFake()
	rt := &countingRuntime{Runtime: fake}

	var seq int
	registrar, err := mcp.NewManagedRegistrar(store.Queries(),
		func() string { seq++; return fmt.Sprintf("srv-%d", seq) },
		func() time.Time { return time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatalf("NewManagedRegistrar: %v", err)
	}

	cfg := Config{
		Runtime:  rt,
		Store:    store.Queries(),
		Posture:  posture,
		Interval: time.Hour,
		Attach:   &AttachDeps{Store: store.Queries(), Endpoints: registrar},
	}
	if posture != container.PostureManual {
		cfg.Rotations = store.Queries()
		cfg.Subnets = testAllocator(t)
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &attachFixture{store: store, fake: fake, rt: rt, r: r}
}

// seed creates an instance with an active generation 1 whose container
// answers on ip, and gives its plugin a manifest that declares a port.
func (f *attachFixture) seed(t *testing.T, instanceID, ip string) {
	t.Helper()
	seedInstance(t, f.store, f.rt, instanceID, "sha256:aaa", "cfg-1")
	if _, err := f.store.DB().Exec(`UPDATE plugins SET manifest_snapshot = ?`, attachManifest); err != nil {
		t.Fatalf("set manifest: %v", err)
	}
	f.setIP(t, instanceID, 1, ip)
}

// switchTo performs what the rotation switch does to the tables, without
// involving Register: generation 2 becomes active on newIP and generation 1
// drains.
func (f *attachFixture) switchTo(t *testing.T, instanceID, newIP string) {
	t.Helper()
	seedGenerationContainer(t, f.store, f.rt, instanceID, 2, GenStarting, "sha256:bbb", "cfg-2", "gen2-token")
	gens := liveGenerations(t, f.store, instanceID)
	for _, gen := range gens {
		next := ""
		switch gen.Generation {
		case 1:
			next = GenDraining
		case 2:
			next = GenActive
		}
		if _, err := f.store.Queries().UpdateContainerGenerationStatus(context.Background(), db.UpdateContainerGenerationStatusParams{
			Status: next, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), ID: gen.ID, ExpectedStatus: gen.Status,
		}); err != nil {
			t.Fatalf("move generation %d to %s: %v", gen.Generation, next, err)
		}
	}
	f.setIP(t, instanceID, 2, newIP)
}

func (f *attachFixture) setIP(t *testing.T, instanceID string, generation int64, ip string) {
	t.Helper()
	gens, err := f.store.Queries().ListContainerGenerationsByInstance(context.Background(), instanceID)
	if err != nil {
		t.Fatalf("list generations: %v", err)
	}
	for _, gen := range gens {
		if gen.Generation == generation && gen.ContainerID != nil {
			f.fake.SetIPAddress(container.ContainerID(*gen.ContainerID), ip)
			return
		}
	}
	t.Fatalf("no container for generation %d", generation)
}

func (f *attachFixture) managedRow(t *testing.T, instanceID string) (db.McpServer, bool) {
	t.Helper()
	rows, err := f.store.Queries().ListManagedMCPServers(context.Background())
	if err != nil {
		t.Fatalf("ListManagedMCPServers: %v", err)
	}
	for _, row := range rows {
		if row.PluginInstanceID != nil && *row.PluginInstanceID == instanceID {
			return row, true
		}
	}
	return db.McpServer{}, false
}

func TestAttachPass_RegistersActiveGeneration(t *testing.T) {
	f := newAttachFixture(t, container.PostureRootlessPodman)
	f.seed(t, "inst1", "10.83.4.2")

	actions, err := f.r.AttachPass(context.Background())
	if err != nil {
		t.Fatalf("AttachPass: %v", err)
	}
	if len(actions) != 1 || actions[0].Kind != AttachRegister {
		t.Fatalf("actions = %+v, want one register", actions)
	}

	row, ok := f.managedRow(t, "inst1")
	if !ok {
		t.Fatal("no managed row after AttachPass")
	}
	if row.Url != "http://10.83.4.2:8080/" {
		t.Errorf("Url = %q, want http://10.83.4.2:8080/", row.Url)
	}
	if row.Name != "i-inst1" {
		t.Errorf("Name = %q, want the instance's stable name i-inst1", row.Name)
	}
	if got := mcp.TrustTierOf(row); got != mcp.TrustTierManaged {
		t.Errorf("TrustTierOf = %q, want managed", got)
	}

	again, err := f.r.AttachPass(context.Background())
	if err != nil {
		t.Fatalf("second AttachPass: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second pass applied %+v, want none (converged)", again)
	}
}

// The window this pins: the switch committed, the process died before
// Register ran. The next pass must repoint from state alone.
func TestAttachPass_ConvergesAfterSwitchWithoutRegister(t *testing.T) {
	f := newAttachFixture(t, container.PostureRootlessPodman)
	f.seed(t, "inst1", "10.83.4.2")
	if _, err := f.r.AttachPass(context.Background()); err != nil {
		t.Fatalf("first AttachPass: %v", err)
	}
	before, _ := f.managedRow(t, "inst1")

	f.switchTo(t, "inst1", "10.83.4.9")

	row, _ := f.managedRow(t, "inst1")
	if row.Url != "http://10.83.4.2:8080/" {
		t.Fatalf("precondition: row repointed without a pass, Url = %q", row.Url)
	}

	if _, err := f.r.AttachPass(context.Background()); err != nil {
		t.Fatalf("AttachPass after switch: %v", err)
	}
	after, _ := f.managedRow(t, "inst1")
	if after.Url != "http://10.83.4.9:8080/" {
		t.Errorf("Url = %q, want http://10.83.4.9:8080/", after.Url)
	}
	if after.ID != before.ID || after.Name != before.Name {
		t.Errorf("rotation changed identity: %s/%s -> %s/%s; only the URL may move", before.ID, before.Name, after.ID, after.Name)
	}
}

func TestAttachPass_NoLiveGenerationDeregistersAndCascadesTools(t *testing.T) {
	f := newAttachFixture(t, container.PostureRootlessPodman)
	f.seed(t, "inst1", "10.83.4.2")
	if _, err := f.r.AttachPass(context.Background()); err != nil {
		t.Fatalf("AttachPass: %v", err)
	}
	row, _ := f.managedRow(t, "inst1")
	if _, err := f.store.DB().Exec(
		`INSERT INTO mcp_tools(id, server_id, name, description, input_schema, created_at) VALUES ('t1', ?, 'send', '', '{}', '2026-08-01T00:00:00Z')`,
		row.ID,
	); err != nil {
		t.Fatalf("insert tool: %v", err)
	}

	if _, err := f.store.DB().Exec(`UPDATE plugin_container_generations SET status = 'stopped'`); err != nil {
		t.Fatalf("retire generation: %v", err)
	}

	actions, err := f.r.AttachPass(context.Background())
	if err != nil {
		t.Fatalf("AttachPass: %v", err)
	}
	if len(actions) != 1 || actions[0].Kind != AttachDeregister {
		t.Fatalf("actions = %+v, want one deregister", actions)
	}
	if _, ok := f.managedRow(t, "inst1"); ok {
		t.Error("managed row survived the retire")
	}
	var tools int
	if err := f.store.DB().QueryRow(`SELECT COUNT(*) FROM mcp_tools WHERE server_id = ?`, row.ID).Scan(&tools); err != nil {
		t.Fatalf("count tools: %v", err)
	}
	if tools != 0 {
		t.Errorf("mcp_tools rows = %d, want 0 (cascade)", tools)
	}
}

func TestAttachPass_HoldsWhenAddressUnknown(t *testing.T) {
	f := newAttachFixture(t, container.PostureRootlessPodman)
	f.seed(t, "inst1", "")

	actions, err := f.r.AttachPass(context.Background())
	if err != nil {
		t.Fatalf("AttachPass: %v", err)
	}
	if len(actions) != 0 {
		t.Errorf("actions = %+v, want none while the container has no address", actions)
	}
	if _, ok := f.managedRow(t, "inst1"); ok {
		t.Error("a row was registered without an address")
	}
}

func TestAttachPass_ManualPosture(t *testing.T) {
	tests := []struct {
		name     string
		version  int64 // desired row's generation; the container is labelled 1
		wantAttd bool
	}{
		{name: "matched attaches", version: 1, wantAttd: true},
		{name: "wrong_generation does not attach", version: 2, wantAttd: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newAttachFixture(t, container.PostureManual)
			f.seed(t, "inst1", "10.83.4.2")
			if _, err := f.store.DB().Exec(`UPDATE plugin_containers SET version = ?`, tt.version); err != nil {
				t.Fatalf("set desired generation: %v", err)
			}

			writesBefore := f.rt.writes()
			if _, err := f.r.AttachPass(context.Background()); err != nil {
				t.Fatalf("AttachPass: %v", err)
			}
			row, ok := f.managedRow(t, "inst1")
			if ok != tt.wantAttd {
				t.Fatalf("managed row present = %v, want %v", ok, tt.wantAttd)
			}
			if ok && row.Url != "http://10.83.4.2:8080/" {
				t.Errorf("Url = %q, want http://10.83.4.2:8080/", row.Url)
			}
			if got := f.rt.writes() - writesBefore; got != 0 {
				t.Errorf("manual posture made %d socket writes, want 0", got)
			}
		})
	}
}

func TestAttachPass_ManualPostureWithdrawsRowOnceGenerationDrifts(t *testing.T) {
	f := newAttachFixture(t, container.PostureManual)
	f.seed(t, "inst1", "10.83.4.2")
	if _, err := f.r.AttachPass(context.Background()); err != nil {
		t.Fatalf("AttachPass: %v", err)
	}
	if _, ok := f.managedRow(t, "inst1"); !ok {
		t.Fatal("matched container was not attached")
	}

	if _, err := f.store.DB().Exec(`UPDATE plugin_containers SET version = 2`); err != nil {
		t.Fatalf("drift desired generation: %v", err)
	}
	if _, err := f.r.AttachPass(context.Background()); err != nil {
		t.Fatalf("AttachPass: %v", err)
	}
	if _, ok := f.managedRow(t, "inst1"); ok {
		t.Error("row for a wrong_generation container was not withdrawn")
	}
}

// The run loop owns the ordering: attach runs after the rotation half of the
// same pass, so one pass is enough to expose a generation that is already
// active.
func TestRunPass_AttachesAfterRotationHalf(t *testing.T) {
	f := newAttachFixture(t, container.PostureRootlessPodman)
	f.seed(t, "inst1", "10.83.4.2")

	if err := f.r.runPass(context.Background()); err != nil {
		t.Fatalf("runPass: %v", err)
	}
	if _, ok := f.managedRow(t, "inst1"); !ok {
		t.Error("runPass left the active generation without a managed row")
	}
}

func TestNew_AttachRequiresRotationsOutsideManual(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	_, err = New(Config{
		Runtime: newTestFake(),
		Store:   store.Queries(),
		Posture: container.PostureRootlessPodman,
		Attach:  &AttachDeps{Store: store.Queries(), Endpoints: &mcp.ManagedRegistrar{}},
	})
	if err == nil {
		t.Fatal("New() = nil error, want Attach without Rotations refused")
	}
}
