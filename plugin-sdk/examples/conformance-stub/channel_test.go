package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/channelext"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient/hostclienttest"
)

const testActor = "actor-1"

type channelHarness struct {
	url  string
	host *hostclienttest.Server
	stub *stub
	// release unblocks every resolution waiting out its delay.
	release chan struct{}
	cancel  context.CancelFunc
}

func newChannelHarness(t *testing.T, mode string, hostOpts ...hostclienttest.Option) *channelHarness {
	t.Helper()
	host := hostclienttest.NewServer(t, hostOpts...)
	client, err := hostclient.New()
	if err != nil {
		t.Fatalf("hostclient.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cfg := Config{
		Profiles:    Profiles{HumanChannel: true},
		Assurance:   "authenticated",
		AutoResolve: mode,
		ActorID:     testActor,
		DataDir:     t.TempDir(),
	}
	s, err := newStub(ctx, cfg, client)
	if err != nil {
		cancel()
		t.Fatalf("newStub: %v", err)
	}
	h := &channelHarness{host: host, stub: s, release: make(chan struct{}), cancel: cancel}
	s.Channel.wait = func(ctx context.Context, _ time.Duration) error {
		select {
		case <-h.release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ts := httptest.NewServer(s.Handler)
	h.url = ts.URL

	// Cleanup is LIFO: stop the server, cancel, drain resolutions, then close.
	t.Cleanup(func() { _ = s.Close() })
	t.Cleanup(cancel)
	t.Cleanup(ts.Close)
	return h
}

type taskEnvelope struct {
	TaskID string `json:"taskId"`
	Status string `json:"status"`
	Result *struct {
		OptionID        string `json:"optionId"`
		ActorExternalID string `json:"actorExternalId"`
	} `json:"result"`
}

func (h *channelHarness) request(t *testing.T, options ...map[string]string) string {
	t.Helper()
	var env taskEnvelope
	mustUnmarshal(t, rpc(t, h.url, "channel/request", map[string]any{
		"target":  map[string]string{"delivery": "direct", "address": "someone"},
		"message": "proceed?",
		"options": options,
	}), &env)
	if env.Status != "working" {
		t.Fatalf("new task status = %q, want working", env.Status)
	}
	return env.TaskID
}

func (h *channelHarness) task(t *testing.T, id string) taskEnvelope {
	t.Helper()
	var env taskEnvelope
	mustUnmarshal(t, rpc(t, h.url, "tasks/get", map[string]string{"taskId": id}), &env)
	return env
}

// settle lets every pending resolution run to completion.
func (h *channelHarness) settle() {
	close(h.release)
	h.stub.Channel.Wait()
}

func authorizeCalls(h *channelHarness) int {
	n := 0
	for _, c := range h.host.Calls() {
		if c.Method == "host/authorize_actor" {
			n++
		}
	}
	return n
}

var (
	optApprove = map[string]string{"id": "approve", "label": "Approve"}
	optReject  = map[string]string{"id": "reject", "label": "Reject"}
)

func TestAutoResolve_AuthorizedActorCompletesTask(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		options    []map[string]string
		wantOption string
	}{
		{name: "first option", mode: autoResolveFirstOption, options: []map[string]string{optApprove, optReject}, wantOption: "approve"},
		{name: "reject picks the refusing option", mode: autoResolveReject, options: []map[string]string{optApprove, optReject}, wantOption: "reject"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newChannelHarness(t, tc.mode, hostclienttest.WithAuthorizedActor(testActor, "user-1"))
			id := h.request(t, tc.options...)
			h.settle()

			got := h.task(t, id)
			if got.Status != "completed" {
				t.Fatalf("status = %q, want completed", got.Status)
			}
			if got.Result == nil || got.Result.OptionID != tc.wantOption || got.Result.ActorExternalID != testActor {
				t.Errorf("result = %+v, want option %q by %q", got.Result, tc.wantOption, testActor)
			}
			if n := authorizeCalls(h); n != 1 {
				t.Errorf("authorize_actor calls = %d, want 1", n)
			}
		})
	}
}

func TestAutoResolve_AuthorizesBeforeCompleting(t *testing.T) {
	var mu sync.Mutex
	var sawStatusAtAuthorize string
	var h *channelHarness
	h = newChannelHarness(t, autoResolveFirstOption, hostclienttest.WithAuthorizePolicy(
		func(requestID, actorExternalID string) (bool, string) {
			mu.Lock()
			defer mu.Unlock()
			// The task must still be working when the host is asked.
			task, err := h.stub.Channel.tasks.Get(context.Background(), requestID)
			if err != nil {
				t.Errorf("get task in policy: %v", err)
			}
			sawStatusAtAuthorize = string(task.Status)
			return actorExternalID == testActor, "user-1"
		}))
	id := h.request(t, optApprove)
	h.settle()

	mu.Lock()
	defer mu.Unlock()
	if sawStatusAtAuthorize != "working" {
		t.Errorf("task status when authorize_actor ran = %q, want working", sawStatusAtAuthorize)
	}
	if got := h.task(t, id).Status; got != "completed" {
		t.Errorf("status = %q, want completed", got)
	}
}

func TestAutoResolve_UnauthorizedActorLeavesTaskWorking(t *testing.T) {
	h := newChannelHarness(t, autoResolveFirstOption) // no actor is authorized
	id := h.request(t, optApprove)
	h.settle()

	if got := h.task(t, id); got.Status != "working" || got.Result != nil {
		t.Errorf("task = %+v, want working with no result", got)
	}
	if n := authorizeCalls(h); n != 1 {
		t.Errorf("authorize_actor calls = %d, want 1", n)
	}
}

func TestAutoResolve_NoAnswerAvailableLeavesTaskWorkingWithoutAuthorizing(t *testing.T) {
	h := newChannelHarness(t, autoResolveReject, hostclienttest.WithAuthorizedActor(testActor, "user-1"))
	id := h.request(t, optApprove) // nothing here reads as a refusal
	h.settle()

	if got := h.task(t, id).Status; got != "working" {
		t.Errorf("status = %q, want working", got)
	}
	if n := authorizeCalls(h); n != 0 {
		t.Errorf("authorize_actor calls = %d, want 0", n)
	}
}

func TestAutoResolve_OffNeverResolves(t *testing.T) {
	h := newChannelHarness(t, autoResolveOff, hostclienttest.WithAuthorizedActor(testActor, "user-1"))
	id := h.request(t, optApprove)
	h.settle()

	if got := h.task(t, id).Status; got != "working" {
		t.Errorf("status = %q, want working", got)
	}
	if n := authorizeCalls(h); n != 0 {
		t.Errorf("authorize_actor calls = %d, want 0", n)
	}
}

func TestAutoResolve_CancelledTaskStaysCancelled(t *testing.T) {
	t.Run("cancelled while waiting", func(t *testing.T) {
		h := newChannelHarness(t, autoResolveFirstOption, hostclienttest.WithAuthorizedActor(testActor, "user-1"))
		id := h.request(t, optApprove)

		var cancelled taskEnvelope
		mustUnmarshal(t, rpc(t, h.url, "tasks/cancel", map[string]string{"taskId": id}), &cancelled)
		if cancelled.Status != "cancelled" {
			t.Fatalf("cancel status = %q, want cancelled", cancelled.Status)
		}
		h.settle()

		if got := h.task(t, id).Status; got != "cancelled" {
			t.Errorf("status after settle = %q, want cancelled", got)
		}
		if n := authorizeCalls(h); n != 0 {
			t.Errorf("authorize_actor calls = %d, want 0: the cancel hook should stop the resolution", n)
		}
	})

	t.Run("cancelled behind the resolver's back", func(t *testing.T) {
		// Bypasses the cancel hook to prove the store itself refuses to
		// resurrect a terminal task.
		h := newChannelHarness(t, autoResolveFirstOption, hostclienttest.WithAuthorizedActor(testActor, "user-1"))
		id := h.request(t, optApprove)
		if _, err := h.stub.Channel.tasks.Cancel(context.Background(), id); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		h.settle()

		got := h.task(t, id)
		if got.Status != "cancelled" || got.Result != nil {
			t.Errorf("task = %+v, want cancelled with no result", got)
		}
	})
}

func TestResolutionFor(t *testing.T) {
	opts := func(ids ...string) []channelext.RequestOption {
		var out []channelext.RequestOption
		for _, id := range ids {
			out = append(out, channelext.RequestOption{ID: id})
		}
		return out
	}
	tests := []struct {
		name       string
		mode       string
		req        channelext.Request
		wantOK     bool
		wantOption string
		wantBody   string
	}{
		{name: "first option", mode: autoResolveFirstOption, req: channelext.Request{Options: opts("a", "b")}, wantOK: true, wantOption: "a"},
		{name: "first option, schema only", mode: autoResolveFirstOption, req: channelext.Request{RequestedSchema: json.RawMessage(`{}`)}, wantOK: true, wantBody: `{}`},
		{name: "reject finds deny case-insensitively", mode: autoResolveReject, req: channelext.Request{Options: opts("approve", "Deny")}, wantOK: true, wantOption: "Deny"},
		{name: "reject with no refusing option", mode: autoResolveReject, req: channelext.Request{Options: opts("yes", "no")}},
		{name: "reject, schema only", mode: autoResolveReject, req: channelext.Request{RequestedSchema: json.RawMessage(`{}`)}},
		{name: "off", mode: autoResolveOff, req: channelext.Request{Options: opts("a")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolutionFor(tc.mode, tc.req)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if got.OptionID != tc.wantOption || string(got.Content) != tc.wantBody {
				t.Errorf("resolution = %+v, want option %q body %q", got, tc.wantOption, tc.wantBody)
			}
		})
	}
}
