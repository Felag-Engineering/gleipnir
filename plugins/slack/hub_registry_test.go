package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/slack-go/slack/socketmode"

	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	plugintest "github.com/felag-engineering/gleipnir/plugin-sdk/testing"
)

// runnerWithErr returns a fakeSocketModeRunner whose Run exits immediately
// with the given error — simulates an auth failure or network blip.
func runnerWithErr(err error) socketModeFactory {
	return func(_ string) (socketModeRunner, error) {
		return &fakeSocketModeRunner{runErr: err}, nil
	}
}

// blockingRunner returns a factory producing runners that block until ctx is
// done (simulates a healthy long-running Socket Mode connection).
func blockingRunner() socketModeFactory {
	return func(_ string) (socketModeRunner, error) {
		return &channelFakeRunner{events: make(chan socketmode.Event)}, nil
	}
}

// TestHubRegistry_DeadHubIsReplaced is the regression test for the kitchen-sink
// pre-flight bug: a transient Slack failure caused the hub's Run goroutine to
// exit, leaving the dead hubEntry cached in the registry. Every subsequent
// Acquire returned the corpse — its Done channel was already closed, so
// TriggerService.Start returned immediately, and the supervisor spin-looped
// against a dead hub until the plugin was reinstalled.
//
// After the fix, Acquire detects the dead entry and creates a fresh hub.
func TestHubRegistry_DeadHubIsReplaced(t *testing.T) {
	t.Parallel()

	authErr := errors.New("invalid_auth")
	r := newHubRegistry(runnerWithErr(authErr))

	hub1, release1, err := r.Acquire("xapp-1")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	// Wait for the runner's Run goroutine to exit (runErr is returned synchronously).
	select {
	case <-hub1.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("hub1 did not exit within 2s")
	}

	if got := hub1.DoneErr(); got != authErr {
		t.Errorf("hub1.DoneErr() = %v, want %v", got, authErr)
	}

	// Second Acquire under the same token must NOT return the dead hub.
	hub2, release2, err := r.Acquire("xapp-1")
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if hub2 == hub1 {
		t.Fatal("Acquire returned the dead hub instead of a fresh one")
	}

	// Cleanup — order does not matter; the release closures must both be safe.
	release1()
	release2()
}

// TestChannelService_MaintainerRegistersInteractiveHandler verifies that
// ChannelService.Start launches a maintainer goroutine that eventually
// acquires the hub and registers the interactive handler. Before this fix,
// the constructor's one-shot Acquire silently dropped interactive callbacks
// forever if config arrived after construction.
func TestChannelService_MaintainerRegistersInteractiveHandler(t *testing.T) {
	t.Parallel()

	// Stand up a FakeHost that returns a valid app_level_token.
	host := plugintest.NewFakeHost(
		plugintest.WithInstanceConfigJSON(`{"app_level_token":"xapp-test-token"}`),
	)

	hostLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for host: %v", err)
	}
	hostSrv := grpc.NewServer()
	host.Register(hostSrv)
	go func() { _ = hostSrv.Serve(hostLis) }()
	t.Cleanup(hostSrv.Stop)

	hostConn, err := grpc.NewClient(hostLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	t.Cleanup(func() { hostConn.Close() })
	hostClient := hostv1.NewHostServiceClient(hostConn)

	registry := newHubRegistry(blockingRunner())

	cs := NewChannelService(hostClient, registry, nil)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cs.Start(ctx)

	// The maintainer fetches config, acquires hub, registers handler.
	// Poll because the goroutine runs asynchronously.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		registry.mu.Lock()
		entry, ok := registry.hubs["xapp-test-token"]
		registry.mu.Unlock()
		if ok && entry.hub.interactiveHandler.Load() != nil {
			return // success
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("maintainer did not register interactive handler within 2s")
}

// TestHubRegistry_LateReleaseDoesNotEvictSuccessor verifies the generation-safe
// release closure. After Acquire detects a dead hub and swaps in a replacement,
// a late release call from the original generation must NOT evict the new
// entry from the map — otherwise a subsequent Acquire would unnecessarily
// create a third hub.
func TestHubRegistry_LateReleaseDoesNotEvictSuccessor(t *testing.T) {
	t.Parallel()

	// First factory call: dead runner. Second factory call: healthy runner.
	calls := 0
	factory := func(_ string) (socketModeRunner, error) {
		calls++
		if calls == 1 {
			return &fakeSocketModeRunner{runErr: errors.New("first death")}, nil
		}
		return &channelFakeRunner{events: make(chan socketmode.Event)}, nil
	}
	r := newHubRegistry(factory)

	hub1, release1, err := r.Acquire("xapp-1")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	// Wait for hub1 to die.
	select {
	case <-hub1.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("hub1 did not exit within 2s")
	}

	// Acquire again — gets a fresh hub2 (the healthy one).
	hub2, release2, err := r.Acquire("xapp-1")
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if hub2 == hub1 {
		t.Fatal("Acquire returned the dead hub")
	}

	// Late release of the dead hub1's ref. Must not evict hub2 from the map.
	release1()

	// A third Acquire should return hub2 (still the current entry), not create a
	// third hub. If release1 wrongly evicted hub2, the factory would be called
	// a third time.
	hub3, release3, err := r.Acquire("xapp-1")
	if err != nil {
		t.Fatalf("third Acquire: %v", err)
	}
	if hub3 != hub2 {
		t.Errorf("late release evicted the live successor: hub3 != hub2 (factory called %d times, want 2)", calls)
	}

	release2()
	release3()
}
