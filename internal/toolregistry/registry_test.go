package toolregistry_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/felag-engineering/gleipnir/internal/toolregistry"
)

func TestReserve_Empty_Succeeds(t *testing.T) {
	r := toolregistry.New()
	src := toolregistry.Source{Kind: toolregistry.KindMCP, Name: "server-a"}

	if err := r.Reserve("server-a.tool-1", src); err != nil {
		t.Fatalf("Reserve on empty registry: %v", err)
	}

	got, ok := r.Lookup("server-a.tool-1")
	if !ok {
		t.Fatal("Lookup returned not-found after Reserve")
	}
	if got != src {
		t.Errorf("Lookup = %v, want %v", got, src)
	}
}

func TestReserve_SameOwner_Idempotent(t *testing.T) {
	r := toolregistry.New()
	src := toolregistry.Source{Kind: toolregistry.KindMCP, Name: "server-a"}

	if err := r.Reserve("server-a.tool-1", src); err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	// Second Reserve with the same owner must succeed without error.
	if err := r.Reserve("server-a.tool-1", src); err != nil {
		t.Fatalf("idempotent Reserve: %v", err)
	}
}

func TestReserve_DifferentOwner_ReturnsConflictError(t *testing.T) {
	r := toolregistry.New()
	first := toolregistry.Source{Kind: toolregistry.KindMCP, Name: "server-a"}
	second := toolregistry.Source{Kind: toolregistry.KindPlugin, Name: "plugin-x"}

	if err := r.Reserve("server-a.tool-1", first); err != nil {
		t.Fatalf("first Reserve: %v", err)
	}

	err := r.Reserve("server-a.tool-1", second)
	if err == nil {
		t.Fatal("Reserve with different owner: expected error, got nil")
	}
	if !errors.Is(err, toolregistry.ErrConflict) {
		t.Errorf("errors.Is(err, ErrConflict) = false, want true")
	}

	var ce *toolregistry.ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("errors.As(*ConflictError) = false")
	}
	if ce.DotName != "server-a.tool-1" {
		t.Errorf("ConflictError.DotName = %q, want server-a.tool-1", ce.DotName)
	}
	if ce.Existing != first {
		t.Errorf("ConflictError.Existing = %v, want %v", ce.Existing, first)
	}
}

func TestReserveBulk_AllOrNothing(t *testing.T) {
	r := toolregistry.New()
	plugin := toolregistry.Source{Kind: toolregistry.KindPlugin, Name: "plug-a"}
	mcp := toolregistry.Source{Kind: toolregistry.KindMCP, Name: "srv"}

	// Pre-claim the second name with a different owner.
	if err := r.Reserve("srv.tool-2", mcp); err != nil {
		t.Fatalf("pre-reserve: %v", err)
	}

	entries := []toolregistry.Reservation{
		{DotName: "plug-a.tool-1", Owner: plugin},
		{DotName: "srv.tool-2", Owner: plugin}, // conflicts with mcp
		{DotName: "plug-a.tool-3", Owner: plugin},
	}
	err := r.ReserveBulk(entries)
	if err == nil {
		t.Fatal("ReserveBulk: expected error for conflict, got nil")
	}
	if !errors.Is(err, toolregistry.ErrConflict) {
		t.Errorf("errors.Is(err, ErrConflict) = false")
	}

	// Rollback must have released plug-a.tool-1 (the partial claim made before the conflict).
	snap := r.Snapshot()
	if _, ok := snap["plug-a.tool-1"]; ok {
		t.Error("plug-a.tool-1 should have been rolled back, but it's still reserved")
	}
	// plug-a.tool-3 was never claimed (conflict stopped the loop), so it must be absent too.
	if _, ok := snap["plug-a.tool-3"]; ok {
		t.Error("plug-a.tool-3 should not be reserved")
	}
	// srv.tool-2 must still belong to the original owner.
	if owner, ok := snap["srv.tool-2"]; !ok || owner != mcp {
		t.Errorf("srv.tool-2 owner = %v (ok=%v), want %v", owner, ok, mcp)
	}
}

func TestRelease_OnlyByOwner(t *testing.T) {
	r := toolregistry.New()
	owner := toolregistry.Source{Kind: toolregistry.KindMCP, Name: "srv"}
	other := toolregistry.Source{Kind: toolregistry.KindPlugin, Name: "plug"}

	if err := r.Reserve("srv.tool-1", owner); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	// Release by a non-owner must be a no-op.
	r.Release("srv.tool-1", other)
	if _, ok := r.Lookup("srv.tool-1"); !ok {
		t.Error("Release by non-owner removed the reservation")
	}

	// Release by the actual owner removes it.
	r.Release("srv.tool-1", owner)
	if _, ok := r.Lookup("srv.tool-1"); ok {
		t.Error("Release by owner did not remove the reservation")
	}
}

func TestReleaseAllFor_BulkClears(t *testing.T) {
	r := toolregistry.New()
	plug := toolregistry.Source{Kind: toolregistry.KindPlugin, Name: "plug-a"}
	other := toolregistry.Source{Kind: toolregistry.KindMCP, Name: "srv-b"}

	_ = r.Reserve("plug-a.tool-1", plug)
	_ = r.Reserve("plug-a.tool-2", plug)
	_ = r.Reserve("srv-b.tool-x", other)

	r.ReleaseAllFor(plug)

	snap := r.Snapshot()
	if _, ok := snap["plug-a.tool-1"]; ok {
		t.Error("plug-a.tool-1 should have been released")
	}
	if _, ok := snap["plug-a.tool-2"]; ok {
		t.Error("plug-a.tool-2 should have been released")
	}
	// The unrelated reservation must survive.
	if _, ok := snap["srv-b.tool-x"]; !ok {
		t.Error("srv-b.tool-x should still be reserved after ReleaseAllFor(plug)")
	}
}

func TestConcurrent_Reserve_Race(t *testing.T) {
	const n = 64
	r := toolregistry.New()

	// N goroutines each try to reserve their own distinct dot-name.
	// The test is mainly a -race detector exercise.
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		src := toolregistry.Source{Kind: toolregistry.KindMCP, Name: "srv"}
		dotName := toolregistry.DotName("srv", "tool-"+string(rune('a'+i%26)))
		go func() {
			defer wg.Done()
			_ = r.Reserve(dotName, src)
		}()
	}
	wg.Wait()
}
