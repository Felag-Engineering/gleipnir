package container

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestFake_CreateEnforcesSelfConstraint(t *testing.T) {
	f := NewFake()
	opts := validCreateOptions()
	opts.Privileged = true

	_, err := f.Create(context.Background(), opts)
	var violation *ConstraintViolationError
	if !errors.As(err, &violation) {
		t.Fatalf("Create() error = %v, want *ConstraintViolationError", err)
	}
	if violation.Kind != ViolationPrivileged {
		t.Errorf("violation.Kind = %q, want %q", violation.Kind, ViolationPrivileged)
	}
}

func TestFake_Lifecycle(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	opts := validCreateOptions()
	opts.Labels = map[string]string{"gleipnir.instance_id": "abc123"}

	id, err := f.Create(ctx, opts)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	info, err := f.Inspect(ctx, id)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if info.State != ContainerStateCreated {
		t.Errorf("State after Create = %q, want %q", info.State, ContainerStateCreated)
	}

	if err := f.Start(ctx, id); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	info, _ = f.Inspect(ctx, id)
	if info.State != ContainerStateRunning {
		t.Errorf("State after Start = %q, want %q", info.State, ContainerStateRunning)
	}

	// Remove without force while running must fail — mirrors the real
	// runtime's refusal to remove a live container.
	if err := f.Remove(ctx, id, false); err == nil {
		t.Fatal("Remove(force=false) on a running container = nil error, want error")
	}

	if err := f.Stop(ctx, id, time.Second); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := f.Remove(ctx, id, false); err != nil {
		t.Fatalf("Remove() after Stop error = %v", err)
	}

	if _, err := f.Inspect(ctx, id); err == nil {
		t.Fatal("Inspect() after Remove = nil error, want error (container gone)")
	}
}

func TestFake_ListByLabel(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	mkOpts := func(name, instanceID string) CreateOptions {
		opts := validCreateOptions()
		opts.Name = name
		opts.Labels = map[string]string{"gleipnir.instance_id": instanceID}
		return opts
	}

	idA, err := f.Create(ctx, mkOpts("a", "instance-1"))
	if err != nil {
		t.Fatalf("Create a: %v", err)
	}
	if _, err := f.Create(ctx, mkOpts("b", "instance-2")); err != nil {
		t.Fatalf("Create b: %v", err)
	}

	matches, err := f.ListByLabel(ctx, "gleipnir.instance_id", "instance-1")
	if err != nil {
		t.Fatalf("ListByLabel() error = %v", err)
	}
	if len(matches) != 1 || matches[0].ID != idA {
		t.Fatalf("ListByLabel() = %+v, want exactly container %q", matches, idA)
	}
}

func TestFake_CreateNetworkEnforcesSelfConstraint(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	_, err := f.CreateNetwork(ctx, NetworkOptions{Name: "n", Internal: false})
	var violation *ConstraintViolationError
	if !errors.As(err, &violation) {
		t.Fatalf("CreateNetwork() error = %v, want *ConstraintViolationError", err)
	}
	if violation.Kind != ViolationExternalNetwork {
		t.Errorf("violation.Kind = %q, want %q", violation.Kind, ViolationExternalNetwork)
	}

	id, err := f.CreateNetwork(ctx, NetworkOptions{Name: "n", Internal: true, Labels: map[string]string{"k": "v"}})
	if err != nil {
		t.Fatalf("CreateNetwork() with a valid internal network: %v", err)
	}

	nets, err := f.ListNetworksByLabel(ctx, "k", "v")
	if err != nil {
		t.Fatalf("ListNetworksByLabel() error = %v", err)
	}
	if len(nets) != 1 || nets[0].ID != id {
		t.Fatalf("ListNetworksByLabel() = %+v, want exactly network %q", nets, id)
	}

	if err := f.RemoveNetwork(ctx, id); err != nil {
		t.Fatalf("RemoveNetwork() error = %v", err)
	}
	if err := f.RemoveNetwork(ctx, id); err == nil {
		t.Fatal("RemoveNetwork() on an already-removed network = nil error, want error")
	}
}

// #1021 review item V1: an IPv6-enabled request is refused the same way an
// external one is — before it ever reaches the socket.
func TestFake_CreateNetworkRejectsIPv6(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	_, err := f.CreateNetwork(ctx, NetworkOptions{Name: "n", Internal: true, EnableIPv6: true})
	var violation *ConstraintViolationError
	if !errors.As(err, &violation) {
		t.Fatalf("CreateNetwork() error = %v, want *ConstraintViolationError", err)
	}
	if violation.Kind != ViolationIPv6Enabled {
		t.Errorf("violation.Kind = %q, want %q", violation.Kind, ViolationIPv6Enabled)
	}
}

func TestFake_ConnectAndDisconnectNetwork(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	id, err := f.Create(ctx, validCreateOptions())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	netID, err := f.CreateNetwork(ctx, NetworkOptions{Name: "n", Internal: true})
	if err != nil {
		t.Fatalf("CreateNetwork() error = %v", err)
	}

	pin := net.ParseIP("10.83.4.2")
	if err := f.ConnectNetwork(ctx, netID, id, pin); err != nil {
		t.Fatalf("ConnectNetwork() error = %v", err)
	}
	info, err := f.Inspect(ctx, id)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if len(info.Networks) != 1 || info.Networks[0] != netID {
		t.Fatalf("Networks after ConnectNetwork = %v, want [%v]", info.Networks, netID)
	}
	if got, ok := f.PinnedAddress(id, netID); !ok || !got.Equal(pin) {
		t.Fatalf("PinnedAddress() = (%v, %v), want (%v, true)", got, ok, pin)
	}

	// A second connect to a network already joined ERRORS, matching the real
	// daemon's "endpoint ... already exists in network" refusal — a planner
	// bug that double-attaches must be caught here, not tolerated.
	if err := f.ConnectNetwork(ctx, netID, id, pin); err == nil {
		t.Fatal("ConnectNetwork() on an already-joined network = nil error, want error")
	}
	info, _ = f.Inspect(ctx, id)
	if len(info.Networks) != 1 {
		t.Fatalf("Networks after a rejected repeat ConnectNetwork = %v, want exactly one entry", info.Networks)
	}

	if err := f.DisconnectNetwork(ctx, netID, id); err != nil {
		t.Fatalf("DisconnectNetwork() error = %v", err)
	}
	info, _ = f.Inspect(ctx, id)
	if len(info.Networks) != 0 {
		t.Fatalf("Networks after DisconnectNetwork = %v, want none", info.Networks)
	}
	if _, ok := f.PinnedAddress(id, netID); ok {
		t.Fatal("PinnedAddress() still reports a pin after DisconnectNetwork")
	}

	// Disconnecting again fails: the planner should never ask to leave a
	// network it does not believe it has joined.
	if err := f.DisconnectNetwork(ctx, netID, id); err == nil {
		t.Fatal("DisconnectNetwork() on an unattached network = nil error, want error")
	}
}

func TestFake_ConnectNetworkUnknownContainerOrNetwork(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	id, err := f.Create(ctx, validCreateOptions())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	netID, err := f.CreateNetwork(ctx, NetworkOptions{Name: "n", Internal: true})
	if err != nil {
		t.Fatalf("CreateNetwork() error = %v", err)
	}

	if err := f.ConnectNetwork(ctx, netID, "no-such-container", nil); err == nil {
		t.Error("ConnectNetwork() with an unknown container = nil error, want error")
	}
	if err := f.ConnectNetwork(ctx, "no-such-network", id, nil); err == nil {
		t.Error("ConnectNetwork() with an unknown network = nil error, want error")
	}
	if err := f.DisconnectNetwork(ctx, netID, "no-such-container"); err == nil {
		t.Error("DisconnectNetwork() with an unknown container = nil error, want error")
	}
}

func TestFake_SetLogs(t *testing.T) {
	f := NewFake()
	ctx := context.Background()

	id, err := f.Create(ctx, validCreateOptions())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	f.SetLogs(id, "hello from the plugin")

	rc, err := f.Logs(ctx, id, LogOptions{})
	if err != nil {
		t.Fatalf("Logs() error = %v", err)
	}
	defer rc.Close()

	buf := make([]byte, 64)
	n, _ := rc.Read(buf)
	if got := string(buf[:n]); got != "hello from the plugin" {
		t.Errorf("Logs() content = %q, want %q", got, "hello from the plugin")
	}
}

func TestFake_Close(t *testing.T) {
	f := NewFake()
	if f.Closed() {
		t.Fatal("Closed() = true before Close()")
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !f.Closed() {
		t.Fatal("Closed() = false after Close()")
	}
}
