package container

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReadOnlyRuntime_RejectsWrites(t *testing.T) {
	inner := NewFake()
	ro := NewReadOnlyRuntime(inner)
	ctx := context.Background()

	if _, err := ro.Create(ctx, validCreateOptions()); !errors.Is(err, ErrManualModeWrite) {
		t.Errorf("Create() error = %v, want ErrManualModeWrite", err)
	}
	if err := ro.Start(ctx, "any"); !errors.Is(err, ErrManualModeWrite) {
		t.Errorf("Start() error = %v, want ErrManualModeWrite", err)
	}
	if err := ro.Stop(ctx, "any", time.Second); !errors.Is(err, ErrManualModeWrite) {
		t.Errorf("Stop() error = %v, want ErrManualModeWrite", err)
	}
	if err := ro.Remove(ctx, "any", true); !errors.Is(err, ErrManualModeWrite) {
		t.Errorf("Remove() error = %v, want ErrManualModeWrite", err)
	}
	if _, err := ro.CreateNetwork(ctx, NetworkOptions{Name: "n", Internal: true}); !errors.Is(err, ErrManualModeWrite) {
		t.Errorf("CreateNetwork() error = %v, want ErrManualModeWrite", err)
	}
	if err := ro.RemoveNetwork(ctx, "any"); !errors.Is(err, ErrManualModeWrite) {
		t.Errorf("RemoveNetwork() error = %v, want ErrManualModeWrite", err)
	}

	// None of the rejected calls should have reached inner.
	if list, err := inner.ListByLabel(ctx, "any", "any"); err != nil || len(list) != 0 {
		t.Errorf("inner Fake was mutated despite every write being rejected: list=%v err=%v", list, err)
	}
}

func TestReadOnlyRuntime_DelegatesReads(t *testing.T) {
	inner := NewFake()
	ctx := context.Background()

	id, err := inner.Create(ctx, validCreateOptions())
	if err != nil {
		t.Fatalf("inner.Create: %v", err)
	}

	ro := NewReadOnlyRuntime(inner)

	info, err := ro.Inspect(ctx, id)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if info.ID != id {
		t.Errorf("Inspect().ID = %q, want %q", info.ID, id)
	}

	if _, err := ro.Stats(ctx, id); err != nil {
		t.Errorf("Stats() error = %v", err)
	}
	if _, err := ro.Logs(ctx, id, LogOptions{}); err != nil {
		t.Errorf("Logs() error = %v", err)
	}

	netID, err := inner.CreateNetwork(ctx, NetworkOptions{Name: "net", Internal: true, Labels: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatalf("inner.CreateNetwork: %v", err)
	}
	nets, err := ro.ListNetworksByLabel(ctx, "k", "v")
	if err != nil {
		t.Errorf("ListNetworksByLabel() error = %v", err)
	}
	if len(nets) != 1 || nets[0].ID != netID {
		t.Errorf("ListNetworksByLabel() = %+v, want exactly network %q", nets, netID)
	}

	if err := ro.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
	if !inner.Closed() {
		t.Errorf("Close() did not delegate to inner")
	}
}

func TestReadOnlyRuntime_NilInnerReadsFailClosed(t *testing.T) {
	ro := NewReadOnlyRuntime(nil)
	ctx := context.Background()

	if _, err := ro.Inspect(ctx, "x"); err == nil {
		t.Error("Inspect() with nil inner = nil error, want an error")
	}
	if _, err := ro.ListByLabel(ctx, "k", "v"); err == nil {
		t.Error("ListByLabel() with nil inner = nil error, want an error")
	}
	if err := ro.Close(); err != nil {
		t.Errorf("Close() with nil inner = %v, want nil", err)
	}
}
