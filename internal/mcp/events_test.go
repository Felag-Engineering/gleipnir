package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/semver"
)

// newEventsClient wires a client to a stub events server at the managed
// trust tier — io.gleipnir/* is host-plane, so a test that did not say which
// tier it exercises would be testing the drop (gate.go), not the extension.
func newEventsClient(t *testing.T, stub *FakeEventsServer) *Client {
	t.Helper()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL,
		WithProtocolVersion(ProtocolVersion20260728),
		WithTrustTier(TrustTierManaged),
	)
}

// ExtensionEventsVersion is declared on the wire by both sides (doc §3); it
// must actually be a valid SemVer string, not just a string that looks like
// one.
func TestExtensionEventsVersion_IsValidSemVer(t *testing.T) {
	if !semver.IsValid("v" + ExtensionEventsVersion) {
		t.Errorf("ExtensionEventsVersion %q is not a valid SemVer string", ExtensionEventsVersion)
	}
}

// A modern server/discover declaring the extension populates both
// ProbeResult and the client's own recorded capability.
func TestEventsCapabilityNegotiation(t *testing.T) {
	ctx := context.Background()

	t.Run("declared", func(t *testing.T) {
		stub := NewFakeEventsServer()
		stub.HeartbeatMs = 15000
		stub.MaxBatch = 25
		client := newEventsClient(t, stub)

		probe, err := client.ProbeProtocolVersion(ctx)
		if err != nil {
			t.Fatalf("ProbeProtocolVersion: %v", err)
		}
		if !probe.EventsDeclared {
			t.Fatal("the probe did not report the declared extension")
		}
		if probe.Events.Version != ExtensionEventsVersion {
			t.Errorf("probe version = %q, want %q", probe.Events.Version, ExtensionEventsVersion)
		}
		if probe.Events.Heartbeat != 15*time.Second {
			t.Errorf("probe heartbeat = %v, want 15s", probe.Events.Heartbeat)
		}
		if probe.Events.MaxBatch != 25 {
			t.Errorf("probe maxBatch = %d, want 25", probe.Events.MaxBatch)
		}

		cap, declared := client.EventsCapabilityOf()
		if !declared {
			t.Fatal("the extension was declared but not recorded on the client")
		}
		if cap != probe.Events {
			t.Errorf("client capability = %+v, want %+v (the probe result)", cap, probe.Events)
		}
	})

	t.Run("not declared", func(t *testing.T) {
		stub := NewFakeEventsServer()
		stub.DeclareExtension = false
		client := newEventsClient(t, stub)

		probe, err := client.ProbeProtocolVersion(ctx)
		if err != nil {
			t.Fatalf("ProbeProtocolVersion: %v", err)
		}
		if probe.EventsDeclared {
			t.Error("a server that declared nothing was reported as declaring the extension")
		}
		if _, declared := client.EventsCapabilityOf(); declared {
			t.Error("a server that declared nothing was recorded as declaring the extension")
		}
	})
}

// A malformed capability body must not fail the handshake, must not flip a
// definitively-modern server's era classification to legacy (the #742
// Finding-1 regression class this package already guards for the channel
// extension), and must decode to the zero-valued capability rather than
// something that resolves anything.
func TestEventsCapability_MalformedDeclarationIsFailClosed(t *testing.T) {
	stub := NewFakeEventsServer()
	stub.RawCapability = json.RawMessage(`"not an object"`)
	client := newEventsClient(t, stub)

	probe, err := client.ProbeProtocolVersion(context.Background())
	if err != nil {
		t.Fatalf("ProbeProtocolVersion: %v", err)
	}
	if probe.Era != EraModern {
		t.Errorf("era = %v, want modern regardless of a malformed extension body", probe.Era)
	}

	cap, declared := client.EventsCapabilityOf()
	if !declared {
		t.Fatal("a present-but-broken declaration should still register as declared")
	}
	if cap != (EventsCapability{}) {
		t.Errorf("a malformed declaration decoded to %+v, want the zero value", cap)
	}
}

// io.gleipnir/* is host-plane. An external server declaring it and being
// believed would make a URL an operator pasted in eligible to feed policy
// bindings and launch runs — something no operator designated it for. The
// declaration is dropped at the client, mirroring
// TestExtensionNegotiation_IsReservedToManagedEndpoints (managed_test.go)
// for the channel extension.
func TestEventsExtensionNegotiation_IsReservedToManagedEndpoints(t *testing.T) {
	tests := []struct {
		name         string
		tier         TrustTier
		wantDeclared bool
	}{
		{name: "a managed endpoint's declaration is honoured", tier: TrustTierManaged, wantDeclared: true},
		{name: "an external server's declaration is dropped", tier: TrustTierExternal},
		{name: "an unset tier is treated as external", tier: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := NewFakeEventsServer()
			srv := httptest.NewServer(stub)
			t.Cleanup(srv.Close)

			client := NewClient(srv.URL,
				WithProtocolVersion(ProtocolVersion20260728),
				WithTrustTier(tc.tier),
			)
			probe, err := client.ProbeProtocolVersion(context.Background())
			if err != nil {
				t.Fatalf("ProbeProtocolVersion: %v", err)
			}

			if probe.EventsDeclared != tc.wantDeclared {
				t.Errorf("probe EventsDeclared = %v, want %v", probe.EventsDeclared, tc.wantDeclared)
			}
			_, declared := client.EventsCapabilityOf()
			if declared != tc.wantDeclared {
				t.Errorf("client EventsCapabilityOf declared = %v, want %v", declared, tc.wantDeclared)
			}
			if probe.Era != EraModern {
				t.Errorf("era = %v, want modern regardless of tier", probe.Era)
			}
		})
	}
}

// DiscoverEventKinds must refuse on a legacy pin, naming the transport in
// the error, matching callTasksMethod's refusal shape.
func TestDiscoverEventKinds_RequiresModernTransport(t *testing.T) {
	stub := NewFakeEventsServer()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	client := NewClient(srv.URL, WithTrustTier(TrustTierManaged)) // unpinned ⇒ legacy

	_, err := client.DiscoverEventKinds(context.Background())
	if err == nil {
		t.Fatal("DiscoverEventKinds ran on a legacy-pinned client")
	}
	if !strings.Contains(err.Error(), "2026-07-28") {
		t.Errorf("error %q does not name the required transport", err.Error())
	}
}

// DiscoverEventKinds must refuse on an external-tier client: io.gleipnir/*
// methods are never sent to a server this client has no reason to trust.
func TestDiscoverEventKinds_RequiresManagedTier(t *testing.T) {
	stub := NewFakeEventsServer()
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	client := NewClient(srv.URL, WithProtocolVersion(ProtocolVersion20260728)) // external by default

	_, err := client.DiscoverEventKinds(context.Background())
	if err == nil {
		t.Fatal("DiscoverEventKinds ran on an external-tier client")
	}
	if stub.DiscoverCalls != 0 {
		t.Errorf("events/discover reached the server %d times, want 0 — the refusal must happen client-side", stub.DiscoverCalls)
	}
}

// The round trip: a server declares several kinds, each with a binding
// schema and an operator set, and the client returns them in server order
// with the documented bounds applied.
func TestDiscoverEventKinds_RoundTrip(t *testing.T) {
	stub := NewFakeEventsServer()
	stub.Kinds = []EventKind{
		{
			Kind:          "issue.opened",
			Guidance:      "Fires when an issue is opened.",
			BindingSchema: json.RawMessage(`{"type":"object","properties":{"priority":{"type":"string"}}}`),
			Operators:     map[string][]string{"priority": {"eq", "in"}},
		},
		{
			Kind:          "issue.closed",
			Guidance:      "Fires when an issue is closed.",
			BindingSchema: json.RawMessage(`{"type":"object"}`),
			Operators:     map[string][]string{"label": {"eq"}},
		},
		{
			Kind:     "release.published",
			Guidance: "Fires when a release is published.",
		},
	}
	client := newEventsClient(t, stub)

	if _, err := client.ProbeProtocolVersion(context.Background()); err != nil {
		t.Fatalf("ProbeProtocolVersion: %v", err)
	}

	kinds, err := client.DiscoverEventKinds(context.Background())
	if err != nil {
		t.Fatalf("DiscoverEventKinds: %v", err)
	}
	if stub.DiscoverCalls != 1 {
		t.Errorf("events/discover called %d times, want 1", stub.DiscoverCalls)
	}
	if len(kinds) != 3 {
		t.Fatalf("got %d kinds, want 3", len(kinds))
	}

	wantKinds := []string{"issue.opened", "issue.closed", "release.published"}
	for i, want := range wantKinds {
		if kinds[i].Kind != want {
			t.Errorf("kinds[%d].Kind = %q, want %q (server order must be preserved)", i, kinds[i].Kind, want)
		}
	}

	if got := kinds[0].Operators["priority"]; len(got) != 2 || got[0] != "eq" || got[1] != "in" {
		t.Errorf("kinds[0].Operators[priority] = %v, want [eq in]", got)
	}
	if string(kinds[0].BindingSchema) != `{"type":"object","properties":{"priority":{"type":"string"}}}` {
		t.Errorf("kinds[0].BindingSchema = %s, want the schema unchanged", kinds[0].BindingSchema)
	}
	if len(kinds[2].Operators) != 0 {
		t.Errorf("kinds[2].Operators = %v, want none declared", kinds[2].Operators)
	}
}

// Bounds are applied even when a server declares far more than a realistic
// profile: too many kinds, an oversize binding schema, and an operators map
// wider than the caps allow.
func TestDiscoverEventKinds_BoundsApplied(t *testing.T) {
	stub := NewFakeEventsServer()
	for i := 0; i < maxEventKindsPerResponse+10; i++ {
		stub.Kinds = append(stub.Kinds, EventKind{Kind: "kind", Guidance: "g"})
	}
	oversizeSchema := json.RawMessage(`{"x":"` + strings.Repeat("a", maxEventBindingSchemaBytes) + `"}`)
	longName := strings.Repeat("x", maxEventKindNameLen+50)
	operators := map[string][]string{}
	for i := 0; i < maxEventOperatorFields+5; i++ {
		operators[strings.Repeat("f", i+1)] = []string{"eq"}
	}
	stub.Kinds[0] = EventKind{
		Kind:          longName,
		Guidance:      strings.Repeat("g", maxEventGuidanceLen+100),
		BindingSchema: oversizeSchema,
		Operators:     operators,
	}
	client := newEventsClient(t, stub)
	if _, err := client.ProbeProtocolVersion(context.Background()); err != nil {
		t.Fatalf("ProbeProtocolVersion: %v", err)
	}

	kinds, err := client.DiscoverEventKinds(context.Background())
	if err != nil {
		t.Fatalf("DiscoverEventKinds: %v", err)
	}
	if len(kinds) != maxEventKindsPerResponse {
		t.Errorf("got %d kinds, want the capped %d", len(kinds), maxEventKindsPerResponse)
	}
	const ellipsisOverhead = len("…") // truncateForLog appends this marker on truncation
	if len(kinds[0].Kind) > maxEventKindNameLen+ellipsisOverhead {
		t.Errorf("kind name is %d bytes, want at most %d", len(kinds[0].Kind), maxEventKindNameLen+ellipsisOverhead)
	}
	if len(kinds[0].Guidance) > maxEventGuidanceLen+ellipsisOverhead {
		t.Errorf("guidance is %d bytes, want at most %d", len(kinds[0].Guidance), maxEventGuidanceLen+ellipsisOverhead)
	}
	if kinds[0].BindingSchema != nil {
		t.Error("an oversize binding schema was not dropped")
	}
	if len(kinds[0].Operators) > maxEventOperatorFields {
		t.Errorf("got %d operator fields, want at most %d", len(kinds[0].Operators), maxEventOperatorFields)
	}
}
