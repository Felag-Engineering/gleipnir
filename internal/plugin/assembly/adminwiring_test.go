//go:build substratev2

package assembly

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/model"
	"github.com/felag-engineering/gleipnir/internal/testutil"
	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

type fakeDiscoverer struct {
	err     error
	refresh []string
}

func (f *fakeDiscoverer) RefreshTools(_ context.Context, serverID string) (mcp.ToolDiff, error) {
	f.refresh = append(f.refresh, serverID)
	return mcp.ToolDiff{}, f.err
}

func newManagedEndpoints(t *testing.T, discoverer toolDiscoverer, arbiter *toolregistry.Registry) (*managedEndpoints, func(instanceID string)) {
	t.Helper()
	store := testutil.NewTestStore(t)
	q := store.Queries()
	registrar, err := mcp.NewManagedRegistrar(q, model.NewULID, nil)
	if err != nil {
		t.Fatalf("NewManagedRegistrar: %v", err)
	}
	return &managedEndpoints{registrar: registrar, servers: q, tools: discoverer, arbiter: arbiter},
		func(instanceID string) { seedInstance(t, q, instanceID, v2ToolManifest) }
}

func TestManagedEndpoints_Register(t *testing.T) {
	tests := []struct {
		name        string
		discoverErr error
	}{
		{name: "discovers the tools of the endpoint it registered"},
		{name: "a failed discovery does not withdraw the endpoint", discoverErr: errors.New("plugin not answering yet")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			discoverer := &fakeDiscoverer{err: tt.discoverErr}
			endpoints, seed := newManagedEndpoints(t, discoverer, nil)
			seed("inst-1")

			srv, err := endpoints.Register(context.Background(), mcp.ManagedEndpoint{
				InstanceID: "inst-1", InstanceName: "inst-1", URL: "http://10.83.0.3:8080/",
			})
			if err != nil {
				t.Fatalf("Register: %v", err)
			}
			if len(discoverer.refresh) != 1 || discoverer.refresh[0] != srv.ID {
				t.Errorf("RefreshTools calls = %v, want exactly one for %s", discoverer.refresh, srv.ID)
			}
		})
	}
}

// Deregister must release the instance's reservations, and must be safe to run
// when the row is already gone: deleting an instance cascades the row away
// before the attach pass ever sees it.
func TestManagedEndpoints_Deregister(t *testing.T) {
	src := toolregistry.Source{Kind: toolregistry.KindMCP, Name: "inst-1"}
	other := toolregistry.Source{Kind: toolregistry.KindMCP, Name: "inst-2"}

	tests := []struct {
		name         string
		register     bool
		wantReleased bool
	}{
		{name: "releases the reservations of a registered endpoint", register: true, wantReleased: true},
		{name: "an endpoint that is already gone is not an error and releases nothing", register: false, wantReleased: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arbiter := toolregistry.New()
			if err := arbiter.Reserve("inst-1.echo", src); err != nil {
				t.Fatalf("Reserve: %v", err)
			}
			if err := arbiter.Reserve("inst-2.echo", other); err != nil {
				t.Fatalf("Reserve: %v", err)
			}
			endpoints, seed := newManagedEndpoints(t, &fakeDiscoverer{}, arbiter)
			seed("inst-1")
			if tt.register {
				if _, err := endpoints.Register(context.Background(), mcp.ManagedEndpoint{
					InstanceID: "inst-1", InstanceName: "inst-1", URL: "http://10.83.0.3:8080/",
				}); err != nil {
					t.Fatalf("Register: %v", err)
				}
			}

			if err := endpoints.Deregister(context.Background(), "inst-1"); err != nil {
				t.Fatalf("Deregister: %v", err)
			}

			_, stillReserved := arbiter.Lookup("inst-1.echo")
			if stillReserved == tt.wantReleased {
				t.Errorf("inst-1.echo reserved = %v, want %v", stillReserved, !tt.wantReleased)
			}
			if _, ok := arbiter.Lookup("inst-2.echo"); !ok {
				t.Error("deregistering inst-1 released another instance's reservation")
			}
		})
	}
}

func TestNamespaceReleaser(t *testing.T) {
	arbiter := toolregistry.New()
	mine := toolregistry.Source{Kind: toolregistry.KindMCP, Name: "inst-1"}
	plugin := toolregistry.Source{Kind: toolregistry.KindPlugin, Name: "inst-1"}
	if err := arbiter.Reserve("inst-1.echo", mine); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := arbiter.Reserve("inst-1.legacy", plugin); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	namespaceReleaser{arbiter: arbiter}.UnregisterInstance(context.Background(), "inst-1")

	if _, ok := arbiter.Lookup("inst-1.echo"); ok {
		t.Error("the instance's MCP reservation survived")
	}
	if _, ok := arbiter.Lookup("inst-1.legacy"); !ok {
		t.Error("released a reservation owned by a different source kind")
	}

	// A nil arbiter (plugins without a shared namespace) must not panic.
	namespaceReleaser{}.UnregisterInstance(context.Background(), "inst-1")
}

type countingKicker struct{ kicks int }

func (k *countingKicker) Kick() { k.kicks++ }

// The admin surface is handed the kicker before the reconciler exists; a kick
// that early is dropped, and once the reconciler is set it is forwarded.
func TestLateKicker(t *testing.T) {
	k := &lateKicker{}
	k.Kick()

	target := &countingKicker{}
	k.set(target)
	k.Kick()

	if target.kicks != 1 {
		t.Errorf("forwarded kicks = %d, want 1 (the early one is dropped)", target.kicks)
	}
}

// The events supervisor is bound after the admin surface is built, so lateEvents
// must tolerate "not bound yet" and then reach whatever is bound.
func TestLateEvents(t *testing.T) {
	a := &Assembly{}
	events := lateEvents{a}

	events.Start(context.Background(), "inst-1")
	events.Restart(context.Background(), "inst-1")
	events.Stop("inst-1")

	log := &stepLog{}
	a.events = fakeEvents{log}
	events.Start(context.Background(), "inst-1")
	events.Restart(context.Background(), "inst-1")
	events.Stop("inst-1")

	want := []string{"events.start:inst-1", "events.restart:inst-1", "events.stop:inst-1"}
	if got := log.snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("supervisor calls = %v, want %v", got, want)
	}
}
