//go:build substratev2

package assembly

import (
	"context"
	"net/http"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/plugin/container"
	"github.com/felag-engineering/gleipnir/internal/plugin/reconciler"
)

// Deleting an instance must leave nothing of it behind: not the managed
// endpoint or its tools, not the tool-name reservation, not the listeners, not
// the container.
//
// The database half is the foreign-key cascade from plugin_instances, which
// the admin delete runs; the in-memory half is the Unregistrar the assembly
// hands the admin surface; the runtime half is the reconciler seeing the
// desired-state row gone. Each is asserted separately because each fails
// differently.
func TestBootV2_InstanceDeleteWithdrawsEverything(t *testing.T) {
	ctx := context.Background()
	f := newE2EFixture(t)
	instanceID := f.createInstance()
	f.boot()
	f.passUntil("the managed endpoint, its tools and both listeners", f.converged(instanceID))

	srv, _ := f.managedServer(instanceID)
	if f.toolCount(srv.ID) == 0 {
		t.Fatal("precondition: the endpoint has no discovered tools")
	}
	if _, ok := f.arbiter.Lookup(e2eInstanceName + ".echo"); !ok {
		t.Fatal("precondition: the tool name is not reserved")
	}
	if n := len(f.managedContainers()); n != 1 {
		t.Fatalf("precondition: %d managed containers, want 1", n)
	}

	rec := f.do(http.MethodDelete, "/plugins/"+f.pluginID+"/instances/"+instanceID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204: %s", rec.Code, rec.Body.String())
	}

	// Synchronous with the delete: no reconcile pass has run yet.
	if _, ok := f.managedServer(instanceID); ok {
		t.Error("the managed mcp_servers row survived the instance")
	}
	if n := f.toolCount(srv.ID); n != 0 {
		t.Errorf("%d mcp_tools rows survived the instance", n)
	}
	if _, ok := f.arbiter.Lookup(e2eInstanceName + ".echo"); ok {
		t.Error("the tool name is still reserved after the instance was deleted")
	}
	if _, err := f.q.GetPluginContainerByInstance(ctx, instanceID); err == nil {
		t.Error("the plugin_containers desired-state row survived the instance")
	}

	// The reconciler converges the world on the new desired state.
	f.passUntil("the container and both listeners gone", func() bool {
		return len(f.managedContainers()) == 0 &&
			f.hostAddr(instanceID) == "" &&
			f.egressAddr(instanceID) == ""
	})
}

func (f *e2eFixture) managedContainers() []container.ContainerInfo {
	f.t.Helper()
	live, err := f.rt.ListByLabel(context.Background(), reconciler.LabelManaged, reconciler.ManagedValue)
	if err != nil {
		f.t.Fatalf("ListByLabel: %v", err)
	}
	return live
}

// addrer is the part of *hostendpoint.ListenerSet the tests read.
type addrer interface {
	Addr(instanceID string) string
}

func (f *e2eFixture) hostAddr(instanceID string) string {
	return f.a.hostListeners.(addrer).Addr(instanceID)
}

func (f *e2eFixture) egressAddr(instanceID string) string {
	return f.a.egressProxy.(addrer).Addr(instanceID)
}
