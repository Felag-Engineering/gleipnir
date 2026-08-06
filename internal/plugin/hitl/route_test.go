package hitl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/plugin/inapptask"
	"github.com/felag-engineering/gleipnir/internal/testutil"
)

// --- fixtures ---------------------------------------------------------------

func newStore(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "hitl.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

// fixture stands up a store with a run to hang tasks off, plus a router wired
// to a real in-app manager and a stub client resolver.
func fixture(t *testing.T, runID string, clients ClientResolver) (*db.Store, *inapptask.Manager, *Router) {
	t.Helper()
	store := newStore(t)
	testutil.InsertPolicy(t, store, "p-"+runID, "policy-"+runID, "webhook", testutil.MinimalWebhookPolicy)
	testutil.InsertRun(t, store, runID, "p-"+runID, model.RunStatusWaitingForFeedback)

	manager := inapptask.NewManager(store.Queries())
	router, err := New(Config{
		Clients:   clients,
		InApp:     manager,
		Completer: manager,
		Tasks:     store.Queries(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store, manager, router
}

// fakeChannel is a channel client whose declaration and behavior are set per
// test. It stands in for a plugin's MCP endpoint everywhere the test is about
// the ROUTING decision rather than the wire.
type fakeChannel struct {
	declared   bool
	capability mcp.ChannelCapability
	status     mcp.TaskStatus
	err        error

	mu   sync.Mutex
	last *mcp.ChannelRequestParams
}

func (f *fakeChannel) ChannelCapabilityOf() (mcp.ChannelCapability, bool) {
	return f.capability, f.declared
}

func (f *fakeChannel) ChannelRequest(_ context.Context, p mcp.ChannelRequestParams) (mcp.TaskStatus, error) {
	f.mu.Lock()
	f.last = &p
	f.mu.Unlock()
	if f.err != nil {
		return mcp.TaskStatus{}, f.err
	}
	return f.status, nil
}

func (f *fakeChannel) lastRequest() (mcp.ChannelRequestParams, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.last == nil {
		return mcp.ChannelRequestParams{}, false
	}
	return *f.last, true
}

// healthyChannel returns a client that declares the current contract version at
// the given assurance and supports both delivery targets.
func healthyChannel(assurance mcp.ChannelAssurance, taskID string) *fakeChannel {
	return &fakeChannel{
		declared: true,
		capability: mcp.ChannelCapability{
			Version:    mcp.ExtensionChannelVersion,
			Assurance:  assurance,
			Deliveries: []mcp.ChannelDelivery{mcp.ChannelDeliveryDirect, mcp.ChannelDeliveryShared},
		},
		status: mcp.TaskStatus{TaskID: taskID, Status: mcp.TaskStatusWorking},
	}
}

// mapResolver resolves instance IDs to fake clients.
type mapResolver map[string]ChannelClient

func (m mapResolver) ChannelClientFor(instanceID string) (ChannelClient, error) {
	client, ok := m[instanceID]
	if !ok {
		return nil, fmt.Errorf("no channel client for instance %s", instanceID)
	}
	return client, nil
}

func pluginEntry(entryID, instanceID, serverID string) Entry {
	return Entry{
		EntryID:    entryID,
		InstanceID: instanceID,
		ServerID:   serverID,
		Request:    true,
		Target:     mcp.ChannelTarget{Delivery: mcp.ChannelDeliveryDirect, Address: "person-7"},
	}
}

func inAppEntry() Entry {
	return Entry{EntryID: "gleipnir.in-app", InApp: true, Request: true}
}

func permissionAsk(runID string) Ask {
	return Ask{
		RunID:   runID,
		Message: "Approve the production deploy?",
		Options: []inapptask.Option{{ID: "approve", Label: "Approve"}, {ID: "reject", Label: "Reject"}},
		Kind:    model.ElicitationKindPermission,
	}
}

func informationAsk(runID string) Ask {
	ask := permissionAsk(runID)
	ask.Message = "Which region should the rollout target?"
	ask.Kind = model.ElicitationKindInformation
	return ask
}

// --- the assurance gate -----------------------------------------------------

// The headline rule: a weak channel may answer a question but may not grant
// permission, and being skipped for permission does not fail the request — the
// next entry gets it.
func TestRoute_AssuranceFallThrough(t *testing.T) {
	tests := []struct {
		name          string
		ask           func(string) Ask
		wantEntry     string
		wantAssurance mcp.ChannelAssurance
		wantSkips     []SkipReason
	}{
		{
			name:          "permission falls through the weak channel",
			ask:           permissionAsk,
			wantEntry:     "entry-strong",
			wantAssurance: mcp.ChannelAssuranceAuthenticated,
			wantSkips:     []SkipReason{SkipAssuranceTooWeak},
		},
		{
			name:          "information is settled by the weak channel",
			ask:           informationAsk,
			wantEntry:     "entry-weak",
			wantAssurance: mcp.ChannelAssuranceWeak,
			wantSkips:     nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			weak := healthyChannel(mcp.ChannelAssuranceWeak, "task-weak")
			strong := healthyChannel(mcp.ChannelAssuranceAuthenticated, "task-strong")
			resolver := mapResolver{"inst-weak": weak, "inst-strong": strong}

			store, _, router := fixture(t, "r-gate", resolver)
			testutil.InsertMcpServer(t, store, "srv-weak", "weak-channel", "http://weak.invalid")
			testutil.InsertMcpServer(t, store, "srv-strong", "strong-channel", "http://strong.invalid")

			routed, err := router.Route(ctx, tc.ask("r-gate"), []Entry{
				pluginEntry("entry-weak", "inst-weak", "srv-weak"),
				pluginEntry("entry-strong", "inst-strong", "srv-strong"),
				inAppEntry(),
			})
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			if routed.EntryID != tc.wantEntry {
				t.Errorf("routed to entry %q, want %q", routed.EntryID, tc.wantEntry)
			}
			if routed.Assurance != tc.wantAssurance {
				t.Errorf("assurance = %q, want %q", routed.Assurance, tc.wantAssurance)
			}
			if got := skipReasons(routed.Skipped); !equalReasons(got, tc.wantSkips) {
				t.Errorf("skips = %v, want %v", got, tc.wantSkips)
			}

			// The kind is still sent to the channel: it renders a permission
			// prompt differently from a form. The gate is host-side, not there.
			chosen := weak
			if tc.wantEntry == "entry-strong" {
				chosen = strong
			}
			sent, ok := chosen.lastRequest()
			if !ok {
				t.Fatal("chosen channel received no request")
			}
			if sent.Kind != mcp.ElicitationKind(tc.ask("r-gate").Kind) {
				t.Errorf("sent kind = %q, want %q", sent.Kind, tc.ask("r-gate").Kind)
			}
		})
	}
}

// Every plugin entry skipped, and the auto-appended in-app entry catches the
// request. This is the case that makes the gate safe to apply strictly: a
// permission request refused by every configured channel is still answerable.
func TestRoute_EmptyAfterFallThroughLandsInApp(t *testing.T) {
	ctx := context.Background()
	resolver := mapResolver{
		"inst-weak":   healthyChannel(mcp.ChannelAssuranceWeak, "task-weak"),
		"inst-notify": &fakeChannel{declared: true, capability: mcp.ChannelCapability{Version: mcp.ExtensionChannelVersion}},
	}
	store, manager, router := fixture(t, "r-fallback", resolver)

	routed, err := router.Route(ctx, permissionAsk("r-fallback"), []Entry{
		pluginEntry("entry-weak", "inst-weak", "srv-weak"),
		pluginEntry("entry-notify", "inst-notify", "srv-notify"),
		inAppEntry(),
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !routed.InApp {
		t.Fatalf("routed to %q, want the in-app entry", routed.EntryID)
	}
	if routed.Assurance != mcp.ChannelAssuranceAuthenticated {
		t.Errorf("in-app assurance = %q, want authenticated", routed.Assurance)
	}
	if got := skipReasons(routed.Skipped); !equalReasons(got, []SkipReason{SkipAssuranceTooWeak, SkipAssuranceTooWeak}) {
		t.Errorf("skips = %v, want two assurance skips", got)
	}

	// It is a real durable task on the shared table, with the NULL server_id
	// that means "resolved internally".
	row, err := store.Queries().GetMCPTask(ctx, routed.RowID)
	if err != nil {
		t.Fatalf("GetMCPTask: %v", err)
	}
	if !inapptask.IsInternal(row) {
		t.Error("in-app task has a server_id; it should be NULL")
	}
	if row.Kind != inapptask.KindChannelRequest {
		t.Errorf("kind = %q, want %q", row.Kind, inapptask.KindChannelRequest)
	}

	// And it answers immediately, with no interval to wait out.
	go func() {
		_ = manager.Complete(ctx, routed.RowID, inapptask.Resolution{OptionID: "approve", ActorExternalID: "user-7"})
	}()
	resolution, err := manager.Await(ctx, inapptask.TaskHandle{ID: routed.RowID, TaskID: routed.TaskID}, 5*time.Second)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if resolution.OptionID != "approve" {
		t.Errorf("resolution option = %q, want approve", resolution.OptionID)
	}
}

// With the in-app fallback disabled, an audience that skips everything has
// nowhere left to go. That is a configuration fault and it fails loudly rather
// than leaving a request nobody is ever asked.
func TestRoute_NoEligibleEntryWhenFallbackDisabled(t *testing.T) {
	ctx := context.Background()
	resolver := mapResolver{"inst-weak": healthyChannel(mcp.ChannelAssuranceWeak, "task-weak")}
	_, _, router := fixture(t, "r-none", resolver)

	routed, err := router.Route(ctx, permissionAsk("r-none"), []Entry{
		pluginEntry("entry-weak", "inst-weak", "srv-weak"),
	})
	if !errors.Is(err, ErrNoEligibleEntry) {
		t.Fatalf("Route error = %v, want ErrNoEligibleEntry", err)
	}
	// The fall-through record survives the failure: "nobody could answer" is
	// only actionable alongside why each candidate could not.
	if got := skipReasons(routed.Skipped); !equalReasons(got, []SkipReason{SkipAssuranceTooWeak}) {
		t.Errorf("skips = %v, want one assurance skip", got)
	}
}

// --- the skip table ---------------------------------------------------------

// Every way an entry can be passed over. All of them are survivable: none
// delivered anything, so trying the next entry cannot double-ask a human.
func TestRoute_SkipReasons(t *testing.T) {
	tests := []struct {
		name   string
		entry  func() Entry
		client ChannelClient
		want   SkipReason
	}{
		{
			name:  "notify-only entry",
			entry: func() Entry { e := pluginEntry("e", "inst", "srv"); e.Request = false; return e },
			want:  SkipNotRequestCapable,
		},
		{
			name:  "no delivery target configured",
			entry: func() Entry { e := pluginEntry("e", "inst", "srv"); e.Target = mcp.ChannelTarget{}; return e },
			want:  SkipNoTarget,
		},
		{
			name:  "no client for the instance",
			entry: func() Entry { e := pluginEntry("e", "missing", "srv"); return e },
			want:  SkipChannelUnavailable,
		},
		{
			name:   "server does not declare the extension",
			entry:  func() Entry { return pluginEntry("e", "inst", "srv") },
			client: &fakeChannel{declared: false},
			want:   SkipExtensionNotDeclared,
		},
		{
			name:  "server declares a major version this host cannot read",
			entry: func() Entry { return pluginEntry("e", "inst", "srv") },
			client: &fakeChannel{declared: true, capability: mcp.ChannelCapability{
				Version:    "2.0.0",
				Assurance:  mcp.ChannelAssuranceAuthenticated,
				Deliveries: []mcp.ChannelDelivery{mcp.ChannelDeliveryDirect},
			}},
			want: SkipVersionUnsupported,
		},
		{
			name:  "malformed declaration resolves nothing",
			entry: func() Entry { return pluginEntry("e", "inst", "srv") },
			client: &fakeChannel{declared: true, capability: mcp.ChannelCapability{
				Version:    mcp.ExtensionChannelVersion,
				Deliveries: []mcp.ChannelDelivery{mcp.ChannelDeliveryDirect},
			}},
			want: SkipAssuranceTooWeak,
		},
		{
			name:  "server does not support the configured delivery",
			entry: func() Entry { return pluginEntry("e", "inst", "srv") },
			client: &fakeChannel{declared: true, capability: mcp.ChannelCapability{
				Version:    mcp.ExtensionChannelVersion,
				Assurance:  mcp.ChannelAssuranceAuthenticated,
				Deliveries: []mcp.ChannelDelivery{mcp.ChannelDeliveryShared},
			}},
			want: SkipDeliveryUnsupported,
		},
		{
			name:  "notify-only server errors on channel/request",
			entry: func() Entry { return pluginEntry("e", "inst", "srv") },
			client: func() ChannelClient {
				c := healthyChannel(mcp.ChannelAssuranceAuthenticated, "task-x")
				c.err = errors.New("method not found")
				return c
			}(),
			want: SkipRequestFailed,
		},
		{
			name:  "entry has no mcp_servers row to poll",
			entry: func() Entry { e := pluginEntry("e", "inst", ""); return e },
			client: func() ChannelClient {
				return healthyChannel(mcp.ChannelAssuranceAuthenticated, "task-x")
			}(),
			want: SkipTaskNotPersisted,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			resolver := mapResolver{}
			if tc.client != nil {
				resolver["inst"] = tc.client
			}
			store, _, router := fixture(t, "r-skip", resolver)
			testutil.InsertMcpServer(t, store, "srv", "channel", "http://channel.invalid")

			routed, err := router.Route(ctx, permissionAsk("r-skip"), []Entry{tc.entry(), inAppEntry()})
			if err != nil {
				t.Fatalf("Route: %v", err)
			}
			if !routed.InApp {
				t.Fatalf("routed to %q, want the in-app fallback", routed.EntryID)
			}
			if got := skipReasons(routed.Skipped); !equalReasons(got, []SkipReason{tc.want}) {
				t.Errorf("skips = %v, want [%s]", got, tc.want)
			}
		})
	}
}

// --- the real client --------------------------------------------------------

// The gate and the persistence over the actual io.gleipnir/channel client, so
// the capability the router reads is one a server really declared through
// server/discover rather than one a fake handed over.
func TestRoute_OverTheRealChannelClient(t *testing.T) {
	ctx := context.Background()

	// A fixed clock so the TTL anchor is asserted exactly rather than within a
	// band. The router converts the server's "time remaining" into an instant,
	// and getting that conversion right is the point of the assertion.
	frozen := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	timeNow = func() time.Time { return frozen }
	t.Cleanup(func() { timeNow = func() time.Time { return time.Now() } })

	stub := mcp.NewFakeChannelServer()
	stub.PollIntervalMs = 4000
	stub.TTLMs = 600000
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	// Probe first, then pin: ProbeProtocolVersion deliberately writes nothing
	// (an unpinned client is the whole point of a probe), so the discovery pass
	// is what teaches the client the channel declaration and the caller is what
	// commits to the version. This is the production order.
	client := mcp.NewClient(srv.URL,
		mcp.WithProtocolVersion(mcp.ProtocolVersion20260728),
		// A channel is a managed plugin endpoint; io.gleipnir/* is not
		// negotiated with external servers (#819).
		mcp.WithTrustTier(mcp.TrustTierManaged),
	)
	probe, err := client.ProbeProtocolVersion(ctx)
	if err != nil {
		t.Fatalf("ProbeProtocolVersion: %v", err)
	}
	if !probe.ChannelDeclared {
		t.Fatal("probe did not see the channel extension declaration")
	}

	store, _, router := fixture(t, "r-real", mapResolver{"inst-real": client})
	testutil.InsertMcpServer(t, store, "srv-real", "channel-plugin", srv.URL)

	routed, err := router.Route(ctx, permissionAsk("r-real"), []Entry{
		pluginEntry("entry-real", "inst-real", "srv-real"),
		inAppEntry(),
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if routed.InApp {
		t.Fatalf("routed in-app; the real channel is authenticated and should have taken it (skips=%+v)", routed.Skipped)
	}
	if routed.Assurance != mcp.ChannelAssuranceAuthenticated {
		t.Errorf("assurance = %q, want authenticated", routed.Assurance)
	}
	if routed.PollInterval != 4*time.Second {
		t.Errorf("poll interval = %s, want 4s", routed.PollInterval)
	}

	row, err := store.Queries().GetMCPTask(ctx, routed.RowID)
	if err != nil {
		t.Fatalf("GetMCPTask: %v", err)
	}
	if inapptask.IsInternal(row) {
		t.Error("plugin-routed task has a NULL server_id; the poller would skip it")
	}
	if row.ServerID == nil || *row.ServerID != "srv-real" {
		t.Errorf("server_id = %v, want srv-real", row.ServerID)
	}
	if row.TaskID != routed.TaskID {
		t.Errorf("row task_id = %q, want %q", row.TaskID, routed.TaskID)
	}
	if row.PollIntervalMs == nil || *row.PollIntervalMs != 4000 {
		t.Errorf("poll_interval_ms = %v, want 4000", row.PollIntervalMs)
	}
	// The server declares TTL as time remaining; the row stores the instant, so
	// a restart hours later reads the same deadline instead of a fresh one.
	if row.ServerTtl == nil {
		t.Fatal("server_ttl is NULL, want the anchored expiry")
	}
	wantTTL := frozen.Add(10 * time.Minute).Format(time.RFC3339Nano)
	if *row.ServerTtl != wantTTL {
		t.Errorf("server_ttl = %q, want %q", *row.ServerTtl, wantTTL)
	}

	asked, ok := stub.LastRequest()
	if !ok {
		t.Fatal("stub received no channel/request")
	}
	if asked.Message != "Approve the production deploy?" {
		t.Errorf("asked %q", asked.Message)
	}
}

// --- exactly-one resolution -------------------------------------------------

// Two completions, one winner. The loser gets a typed conflict rather than
// silently overwriting a decision a human already made.
//
// The row under contention is PLUGIN-routed, which is the point: the CAS is on
// the shared task row, so a poller observing a server-side completion and an
// operator clicking in the UI contend on the same UPDATE regardless of which
// route opened the task.
func TestComplete_DoubleResolutionOneWinner(t *testing.T) {
	ctx := context.Background()
	channel := healthyChannel(mcp.ChannelAssuranceAuthenticated, "task-race")
	store, _, router := fixture(t, "r-race", mapResolver{"inst": channel})
	testutil.InsertMcpServer(t, store, "srv", "channel", "http://channel.invalid")

	routed, err := router.Route(ctx, permissionAsk("r-race"), []Entry{pluginEntry("e", "inst", "srv")})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []error
	)
	for _, actor := range []string{"user-a", "user-b"} {
		wg.Add(1)
		go func(actor string) {
			defer wg.Done()
			err := router.Complete(ctx, routed, inapptask.Resolution{
				OptionID:        "approve",
				ActorExternalID: actor,
			})
			mu.Lock()
			results = append(results, err)
			mu.Unlock()
		}(actor)
	}
	wg.Wait()

	var winners, conflicts int
	for _, err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, inapptask.ErrAlreadyResolved):
			conflicts++
		default:
			t.Errorf("unexpected completion error: %v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("got %d winners and %d conflicts, want exactly 1 of each", winners, conflicts)
	}

	// One decision survives, and it is readable through the shared decoder.
	row, err := store.Queries().GetMCPTask(ctx, routed.RowID)
	if err != nil {
		t.Fatalf("GetMCPTask: %v", err)
	}
	resolution, err := inapptask.DecodeResolution(row)
	if err != nil {
		t.Fatalf("DecodeResolution: %v", err)
	}
	if resolution.ActorExternalID != "user-a" && resolution.ActorExternalID != "user-b" {
		t.Errorf("actor = %q, want one of the two contenders", resolution.ActorExternalID)
	}

	// A third completion long after the fact is still refused, not applied.
	if err := router.Complete(ctx, routed, inapptask.Resolution{OptionID: "reject", ActorExternalID: "user-c"}); !errors.Is(err, inapptask.ErrAlreadyResolved) {
		t.Errorf("late completion error = %v, want ErrAlreadyResolved", err)
	}
}

// --- validation and helpers -------------------------------------------------

func TestRoute_RejectsAnUnanswerableAsk(t *testing.T) {
	tests := []struct {
		name string
		ask  Ask
	}{
		{"no run", Ask{Message: "m", Options: []inapptask.Option{{ID: "a"}}, Kind: model.ElicitationKindPermission}},
		{"no message", Ask{RunID: "r", Options: []inapptask.Option{{ID: "a"}}, Kind: model.ElicitationKindPermission}},
		{"no way to answer", Ask{RunID: "r", Message: "m", Kind: model.ElicitationKindPermission}},
		{"unknown kind", Ask{RunID: "r", Message: "m", Options: []inapptask.Option{{ID: "a"}}, Kind: "urgent"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, router := fixture(t, "r-bad", mapResolver{})
			if _, err := router.Route(context.Background(), tc.ask, []Entry{inAppEntry()}); err == nil {
				t.Fatal("Route accepted an unanswerable ask")
			}
		})
	}
}

func TestTargetFromConfig(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   mcp.ChannelTarget
		ok     bool
	}{
		{"direct", `{"delivery":"direct","address":"person-7"}`, mcp.ChannelTarget{Delivery: mcp.ChannelDeliveryDirect, Address: "person-7"}, true},
		{"shared", `{"delivery":"shared","address":"room-1"}`, mcp.ChannelTarget{Delivery: mcp.ChannelDeliveryShared, Address: "room-1"}, true},
		{"extra keys are the channel's business", `{"delivery":"shared","address":"room-1","thread":"abc"}`, mcp.ChannelTarget{Delivery: mcp.ChannelDeliveryShared, Address: "room-1"}, true},
		{"empty", "", mcp.ChannelTarget{}, false},
		{"empty object", "{}", mcp.ChannelTarget{}, false},
		{"unknown delivery", `{"delivery":"carrier-pigeon","address":"loft-3"}`, mcp.ChannelTarget{}, false},
		{"no address", `{"delivery":"direct"}`, mcp.ChannelTarget{}, false},
		{"not json", "not json", mcp.ChannelTarget{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := TargetFromConfig(tc.config)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Errorf("target = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMajorVersionSupported(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"1.0.0", true},
		{"1.4.2", true},
		{"1", true},
		{" 1.0.0 ", true},
		{"2.0.0", false},
		{"0.9.0", false},
		{"", false},
		{"one.two.three", false},
		{"-1.0.0", false},
	}
	for _, tc := range tests {
		t.Run(tc.version, func(t *testing.T) {
			if got := majorVersionSupported(tc.version); got != tc.want {
				t.Errorf("majorVersionSupported(%q) = %v, want %v", tc.version, got, tc.want)
			}
		})
	}
}

// The form ask: no options, a schema instead. Both legs accept it, so the
// router must not require options.
func TestRoute_FormAsk(t *testing.T) {
	ctx := context.Background()
	_, _, router := fixture(t, "r-form", mapResolver{})

	routed, err := router.Route(ctx, Ask{
		RunID:           "r-form",
		Message:         "Which region?",
		RequestedSchema: json.RawMessage(`{"type":"object","properties":{"region":{"type":"string"}}}`),
		Kind:            model.ElicitationKindInformation,
	}, []Entry{inAppEntry()})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !routed.InApp {
		t.Error("form ask did not reach the in-app channel")
	}
}

func skipReasons(skips []Skip) []SkipReason {
	if len(skips) == 0 {
		return nil
	}
	out := make([]SkipReason, len(skips))
	for i, s := range skips {
		out[i] = s.Reason
	}
	return out
}

func equalReasons(got, want []SkipReason) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
