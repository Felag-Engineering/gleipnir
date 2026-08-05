package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// newChannelClient wires a client to a stub channel server.
func newChannelClient(t *testing.T, stub *FakeChannelServer) *Client {
	t.Helper()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, WithProtocolVersion(ProtocolVersion20260728))
}

// A notification is delivered and acknowledged, and the channel receives the
// target and message unchanged.
func TestChannelNotify(t *testing.T) {
	stub := NewFakeChannelServer()
	client := newChannelClient(t, stub)

	err := client.ChannelNotify(context.Background(), ChannelNotification{
		Target:  ChannelTarget{Delivery: ChannelDeliveryShared, Address: "space-42"},
		Message: "run r-1 finished",
	})
	if err != nil {
		t.Fatalf("ChannelNotify: %v", err)
	}

	got, ok := stub.LastNotification()
	if !ok {
		t.Fatal("the channel received no notification")
	}
	if got.Target.Delivery != ChannelDeliveryShared || got.Target.Address != "space-42" {
		t.Errorf("target = %+v, want shared/space-42", got.Target)
	}
	if got.Message != "run r-1 finished" {
		t.Errorf("message = %q, want the host's text verbatim", got.Message)
	}
}

// A malformed target never reaches the wire: a routing bug should surface at
// the call site, not as a confusing plugin-side error.
func TestChannelNotify_RejectsBadTarget(t *testing.T) {
	stub := NewFakeChannelServer()
	client := newChannelClient(t, stub)

	tests := []struct {
		name   string
		target ChannelTarget
	}{
		{name: "unknown delivery", target: ChannelTarget{Delivery: "dm", Address: "u1"}},
		{name: "empty delivery", target: ChannelTarget{Address: "u1"}},
		{name: "empty address", target: ChannelTarget{Delivery: ChannelDeliveryDirect}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := client.ChannelNotify(context.Background(), ChannelNotification{Target: tc.target, Message: "x"}); err == nil {
				t.Fatal("ChannelNotify accepted an invalid target")
			}
		})
	}
	if n := stub.NotificationCount(); n != 0 {
		t.Errorf("%d invalid notifications reached the channel, want 0", n)
	}
}

// The happy path for an ask: a task handle comes back, the human answers, and
// the resolution decodes to what they chose and who they were.
func TestChannelRequest_CompletesWithAResolution(t *testing.T) {
	ctx := context.Background()
	stub := NewFakeChannelServer()
	client := newChannelClient(t, stub)

	task, err := client.ChannelRequest(ctx, ChannelRequestParams{
		Target:  ChannelTarget{Delivery: ChannelDeliveryDirect, Address: "person-7"},
		Message: "Approve the production deploy?",
		Options: []ChannelOption{{ID: "approve", Label: "Approve"}, {ID: "reject", Label: "Reject"}},
		Kind:    ElicitationKindPermission,
	})
	if err != nil {
		t.Fatalf("ChannelRequest: %v", err)
	}
	if task.TaskID == "" {
		t.Fatal("no task handle returned; the wait would be unaddressable")
	}
	if task.Status != TaskStatusWorking {
		t.Errorf("status = %q, want working", task.Status)
	}

	// The channel got the elicitation-shaped payload, not some channel-specific
	// vocabulary — that shape is the extension's central design claim.
	sent, ok := stub.LastRequest()
	if !ok {
		t.Fatal("the channel received no request")
	}
	if sent.Message != "Approve the production deploy?" {
		t.Errorf("message = %q", sent.Message)
	}
	if len(sent.Options) != 2 || sent.Options[0].ID != "approve" {
		t.Errorf("options = %+v, want both choices in order", sent.Options)
	}
	if sent.Kind != ElicitationKindPermission {
		t.Errorf("kind = %q, want permission", sent.Kind)
	}

	stub.CompleteTask(task.TaskID, "approve", "external-user-9", nil)

	final, err := client.GetTask(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if final.Status != TaskStatusCompleted {
		t.Fatalf("status = %q, want completed", final.Status)
	}

	resolution, err := DecodeChannelResolution(final.Result)
	if err != nil {
		t.Fatalf("DecodeChannelResolution: %v", err)
	}
	if resolution.OptionID != "approve" {
		t.Errorf("option = %q, want approve", resolution.OptionID)
	}
	if resolution.ActorExternalID != "external-user-9" {
		t.Errorf("actor = %q, want the channel's identifier for who acted", resolution.ActorExternalID)
	}
}

// A form ask carries a schema instead of options, and the answer comes back as
// content.
func TestChannelRequest_FormResolution(t *testing.T) {
	ctx := context.Background()
	stub := NewFakeChannelServer()
	client := newChannelClient(t, stub)

	task, err := client.ChannelRequest(ctx, ChannelRequestParams{
		Target:          ChannelTarget{Delivery: ChannelDeliveryDirect, Address: "person-7"},
		Message:         "Which ticket authorizes this?",
		RequestedSchema: json.RawMessage(`{"type":"object","properties":{"ticket":{"type":"string"}}}`),
		Kind:            ElicitationKindInformation,
	})
	if err != nil {
		t.Fatalf("ChannelRequest: %v", err)
	}

	stub.CompleteTask(task.TaskID, "", "external-user-9", json.RawMessage(`{"ticket":"OPS-1"}`))

	final, err := client.GetTask(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	resolution, err := DecodeChannelResolution(final.Result)
	if err != nil {
		t.Fatalf("DecodeChannelResolution: %v", err)
	}
	if string(resolution.Content) != `{"ticket":"OPS-1"}` {
		t.Errorf("content = %s, want the submitted form verbatim", resolution.Content)
	}
}

// Termination is tasks/cancel and nothing else — the extension coins no
// cancellation vocabulary of its own.
func TestChannelRequest_CancelTerminatesTheWait(t *testing.T) {
	ctx := context.Background()
	stub := NewFakeChannelServer()
	client := newChannelClient(t, stub)

	task, err := client.ChannelRequest(ctx, ChannelRequestParams{
		Target:  ChannelTarget{Delivery: ChannelDeliveryShared, Address: "space-1"},
		Message: "Still needed?",
		Options: []ChannelOption{{ID: "yes", Label: "Yes"}},
	})
	if err != nil {
		t.Fatalf("ChannelRequest: %v", err)
	}

	cancelled, err := client.CancelTask(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if cancelled.Status != TaskStatusCancelled {
		t.Errorf("status = %q, want cancelled", cancelled.Status)
	}
	if got := stub.TaskStatusOf(task.TaskID); got != TaskStatusCancelled {
		t.Errorf("server-side status = %q, want cancelled", got)
	}
}

// A server-side TTL expiry is an ordinary terminal task state, and its result
// must not decode into a resolution — "nobody answered" is not an answer.
func TestChannelRequest_TTLExpiry(t *testing.T) {
	ctx := context.Background()
	stub := NewFakeChannelServer()
	stub.TTLMs = 60_000
	client := newChannelClient(t, stub)

	task, err := client.ChannelRequest(ctx, ChannelRequestParams{
		Target:  ChannelTarget{Delivery: ChannelDeliveryDirect, Address: "person-2"},
		Message: "Approve?",
		Options: []ChannelOption{{ID: "yes", Label: "Yes"}},
	})
	if err != nil {
		t.Fatalf("ChannelRequest: %v", err)
	}
	if task.TTL == 0 {
		t.Error("server-declared TTL was dropped; the host cannot surface an effective deadline without it")
	}

	stub.ExpireTask(task.TaskID)

	final, err := client.GetTask(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if final.Status != TaskStatusFailed {
		t.Fatalf("status = %q, want failed", final.Status)
	}
	if !strings.Contains(final.StatusMessage, "ttl expired") {
		t.Errorf("status message = %q, want it to explain the expiry", final.StatusMessage)
	}
	if _, err := DecodeChannelResolution(final.Result); err == nil {
		t.Error("an expired request decoded into a resolution; a missing answer must never read as one")
	}
}

// A request with neither options nor a schema is a notification wearing a
// question's clothes: it would leave a task open forever waiting for an answer
// the human has no way to give.
func TestChannelRequest_RejectsAnUnanswerableAsk(t *testing.T) {
	stub := NewFakeChannelServer()
	client := newChannelClient(t, stub)

	_, err := client.ChannelRequest(context.Background(), ChannelRequestParams{
		Target:  ChannelTarget{Delivery: ChannelDeliveryDirect, Address: "person-1"},
		Message: "FYI",
	})
	if err == nil {
		t.Fatal("ChannelRequest accepted a request with no way to answer it")
	}
	if !strings.Contains(err.Error(), methodChannelNotify) {
		t.Errorf("error = %q, want it to point at %s", err, methodChannelNotify)
	}

	_, err = client.ChannelRequest(context.Background(), ChannelRequestParams{
		Target:  ChannelTarget{Delivery: ChannelDeliveryDirect, Address: "person-1"},
		Options: []ChannelOption{{ID: "y", Label: "Y"}},
	})
	if err == nil {
		t.Fatal("ChannelRequest accepted an empty message")
	}
}

// Both methods require the modern transport: an MRTR/Tasks-era extension over a
// legacy pin would silently drop the task handle.
func TestChannel_RequiresModernTransport(t *testing.T) {
	stub := NewFakeChannelServer()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	client := NewClient(srv.URL) // unpinned ⇒ legacy

	if err := client.ChannelNotify(context.Background(), ChannelNotification{
		Target:  ChannelTarget{Delivery: ChannelDeliveryDirect, Address: "u1"},
		Message: "x",
	}); err == nil {
		t.Error("ChannelNotify ran on a legacy-pinned client")
	}
	if _, err := client.ChannelRequest(context.Background(), ChannelRequestParams{
		Target:  ChannelTarget{Delivery: ChannelDeliveryDirect, Address: "u1"},
		Message: "x",
		Options: []ChannelOption{{ID: "y", Label: "Y"}},
	}); err == nil {
		t.Error("ChannelRequest ran on a legacy-pinned client")
	}
}

// Negotiation happens in the handshake, not by attempting a method and seeing
// what happens — a host that probed by calling would be delivering messages to
// find out whether it could.
func TestChannelCapabilityNegotiation(t *testing.T) {
	ctx := context.Background()

	t.Run("declared", func(t *testing.T) {
		stub := NewFakeChannelServer()
		stub.Assurance = ChannelAssuranceWeak
		stub.Deliveries = []ChannelDelivery{ChannelDeliveryShared}
		client := newChannelClient(t, stub)

		probe, err := client.ProbeProtocolVersion(ctx)
		if err != nil {
			t.Fatalf("ProbeProtocolVersion: %v", err)
		}
		if !probe.ChannelDeclared {
			t.Fatal("the probe did not report the declared extension")
		}

		cap, declared := client.ChannelCapabilityOf()
		if !declared {
			t.Fatal("the extension was declared but not recorded")
		}
		if cap.Assurance != ChannelAssuranceWeak {
			t.Errorf("assurance = %q, want weak", cap.Assurance)
		}
		if !cap.Supports(ChannelDeliveryShared) {
			t.Error("declared shared delivery was not recorded")
		}
		if cap.Supports(ChannelDeliveryDirect) {
			t.Error("undeclared direct delivery reported as supported; the host must not assume a capability")
		}
		if cap.Version != ExtensionChannelVersion {
			t.Errorf("version = %q, want %q", cap.Version, ExtensionChannelVersion)
		}
	})

	t.Run("not declared", func(t *testing.T) {
		stub := NewFakeChannelServer()
		stub.DeclareExtension = false
		client := newChannelClient(t, stub)

		if _, err := client.ProbeProtocolVersion(ctx); err != nil {
			t.Fatalf("ProbeProtocolVersion: %v", err)
		}

		if _, declared := client.ChannelCapabilityOf(); declared {
			t.Error("a server that declared nothing was reported as declaring the extension")
		}
	})

	t.Run("malformed declaration is fail-closed", func(t *testing.T) {
		stub := NewFakeChannelServer()
		stub.RawCapability = json.RawMessage(`"not an object"`)
		client := newChannelClient(t, stub)

		if _, err := client.ProbeProtocolVersion(ctx); err != nil {
			t.Fatalf("ProbeProtocolVersion: %v", err)
		}

		cap, declared := client.ChannelCapabilityOf()
		if !declared {
			t.Fatal("a present-but-broken declaration should still register as declared")
		}
		if cap.Assurance.MayResolve(ElicitationKindInformation) {
			t.Error("a malformed declaration resolved an elicitation; the zero value must resolve nothing")
		}
		if cap.Supports(ChannelDeliveryDirect) || cap.Supports(ChannelDeliveryShared) {
			t.Error("a malformed declaration reported delivery support")
		}
	})
}

// The §4.1 gate: which request kinds a channel may settle is the host's
// decision, and it is asymmetric.
func TestChannelAssurance_MayResolve(t *testing.T) {
	tests := []struct {
		assurance ChannelAssurance
		kind      ElicitationKind
		want      bool
	}{
		{ChannelAssuranceAuthenticated, ElicitationKindPermission, true},
		{ChannelAssuranceAuthenticated, ElicitationKindInformation, true},
		// A forged approval is indistinguishable from a real one afterwards;
		// a wrong value is at least visible in what the agent then does.
		{ChannelAssuranceWeak, ElicitationKindPermission, false},
		{ChannelAssuranceWeak, ElicitationKindInformation, true},
		// Unknown guarantees resolve nothing — guessing upward cannot be undone.
		{ChannelAssurance("verified-ish"), ElicitationKindPermission, false},
		{ChannelAssurance("verified-ish"), ElicitationKindInformation, false},
		{ChannelAssurance(""), ElicitationKindInformation, false},
	}

	for _, tc := range tests {
		t.Run(string(tc.assurance)+"/"+string(tc.kind), func(t *testing.T) {
			if got := tc.assurance.MayResolve(tc.kind); got != tc.want {
				t.Errorf("MayResolve = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseChannelCapability(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		wantAssurance ChannelAssurance
		wantDirect    bool
		wantShared    bool
	}{
		{
			name:          "full declaration",
			raw:           `{"version":"1.0.0","assurance":"authenticated","deliveries":["direct","shared"]}`,
			wantAssurance: ChannelAssuranceAuthenticated,
			wantDirect:    true,
			wantShared:    true,
		},
		{
			name:          "unknown assurance drops to zero",
			raw:           `{"assurance":"pretty-sure","deliveries":["direct"]}`,
			wantAssurance: "",
			wantDirect:    true,
		},
		{
			name:          "unknown delivery is dropped, known ones survive",
			raw:           `{"assurance":"weak","deliveries":["dm","shared"]}`,
			wantAssurance: ChannelAssuranceWeak,
			wantShared:    true,
		},
		{name: "not an object", raw: `[]`},
		{name: "empty object", raw: `{}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cap := parseChannelCapability(json.RawMessage(tc.raw))
			if cap.Assurance != tc.wantAssurance {
				t.Errorf("assurance = %q, want %q", cap.Assurance, tc.wantAssurance)
			}
			if cap.Supports(ChannelDeliveryDirect) != tc.wantDirect {
				t.Errorf("direct support = %v, want %v", cap.Supports(ChannelDeliveryDirect), tc.wantDirect)
			}
			if cap.Supports(ChannelDeliveryShared) != tc.wantShared {
				t.Errorf("shared support = %v, want %v", cap.Supports(ChannelDeliveryShared), tc.wantShared)
			}
		})
	}
}

// A declaration padded with delivery entries is bounded rather than walked in
// full on every routing decision.
func TestParseChannelCapability_BoundsDeliveries(t *testing.T) {
	var entries []string
	for i := 0; i < 500; i++ {
		entries = append(entries, "shared")
	}
	raw, err := json.Marshal(map[string]any{"deliveries": entries})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	cap := parseChannelCapability(raw)
	if len(cap.Deliveries) > maxChannelDeliveries {
		t.Errorf("kept %d deliveries, want at most %d", len(cap.Deliveries), maxChannelDeliveries)
	}
}

func TestDecodeChannelResolution_Errors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "not json", raw: `{nope`},
		// Neither a choice nor content is a result that records no decision.
		// Reading it as an empty resolution would be reading a non-answer as
		// an answer.
		{name: "no decision recorded", raw: `{"actorExternalId":"u1"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeChannelResolution(json.RawMessage(tc.raw)); err == nil {
				t.Error("DecodeChannelResolution accepted an unusable result")
			}
		})
	}
}

// Identifier fields reach audit records and logs, and a plugin is external.
func TestDecodeChannelResolution_BoundsFields(t *testing.T) {
	huge := strings.Repeat("x", 4096)
	raw, err := json.Marshal(channelResolutionWire{OptionID: huge, ActorExternalID: huge})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	resolution, err := DecodeChannelResolution(raw)
	if err != nil {
		t.Fatalf("DecodeChannelResolution: %v", err)
	}
	if len(resolution.OptionID) > maxChannelResolutionFieldLen+len("…") {
		t.Errorf("optionId is %d bytes, want it bounded", len(resolution.OptionID))
	}
	if len(resolution.ActorExternalID) > maxChannelResolutionFieldLen+len("…") {
		t.Errorf("actorExternalId is %d bytes, want it bounded", len(resolution.ActorExternalID))
	}
}
