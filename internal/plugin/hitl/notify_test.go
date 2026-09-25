package hitl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/db"
	"github.com/felag-engineering/gleipnir/internal/mcp"
	"github.com/felag-engineering/gleipnir/internal/plugin/caphealth"
)

// --- fixtures ----------------------------------------------------------------

// fakeNotifyClient is a NotifyClient whose declaration and behavior are set
// per test, mirroring route_test.go's fakeChannel for the Request path.
type fakeNotifyClient struct {
	declared   bool
	capability mcp.ChannelCapability
	err        error
	// block, when non-nil, makes ChannelNotify wait for ctx to end rather than
	// returning immediately -- used to prove the shared timeout, not the fake,
	// bounds the wall clock.
	block bool

	mu     sync.Mutex
	called bool
	got    []mcp.ChannelNotification
}

func (f *fakeNotifyClient) ChannelCapabilityOf() (mcp.ChannelCapability, bool) {
	return f.capability, f.declared
}

func (f *fakeNotifyClient) ChannelNotify(ctx context.Context, n mcp.ChannelNotification) error {
	f.mu.Lock()
	f.called = true
	f.mu.Unlock()

	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	f.got = append(f.got, n)
	f.mu.Unlock()
	return nil
}

func (f *fakeNotifyClient) notifications() []mcp.ChannelNotification {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]mcp.ChannelNotification, len(f.got))
	copy(out, f.got)
	return out
}

func (f *fakeNotifyClient) wasCalled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.called
}

// healthyNotifyClient declares the current contract version, authenticated,
// and supports both delivery targets.
func healthyNotifyClient() *fakeNotifyClient {
	return &fakeNotifyClient{
		declared: true,
		capability: mcp.ChannelCapability{
			Version:    mcp.ExtensionChannelVersion,
			Assurance:  mcp.ChannelAssuranceAuthenticated,
			Deliveries: []mcp.ChannelDelivery{mcp.ChannelDeliveryDirect, mcp.ChannelDeliveryShared},
		},
	}
}

type notifyMapResolver map[string]NotifyClient

func (m notifyMapResolver) ChannelClientFor(instanceID string) (NotifyClient, error) {
	client, ok := m[instanceID]
	if !ok {
		return nil, fmt.Errorf("no notify client for instance %s", instanceID)
	}
	return client, nil
}

// fakeCapabilityChecker reports Serves per instance ID. An instance with no
// entry serves -- mirrors caphealth's "silence is not a fault" rule.
type fakeCapabilityChecker map[string]bool

func (f fakeCapabilityChecker) Serves(instanceID string, _ caphealth.Capability) bool {
	serves, ok := f[instanceID]
	if !ok {
		return true
	}
	return serves
}

// fakeAuditWriter records every InsertPluginAuditEvent call.
type fakeAuditWriter struct {
	mu     sync.Mutex
	events []db.InsertPluginAuditEventParams
}

func (f *fakeAuditWriter) InsertPluginAuditEvent(_ context.Context, arg db.InsertPluginAuditEventParams) (db.PluginAuditEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, arg)
	return db.PluginAuditEvent{}, nil
}

func (f *fakeAuditWriter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func notifyEntry(entryID, instanceID string) Entry {
	return Entry{
		EntryID:    entryID,
		InstanceID: instanceID,
		Notify:     true,
		Target:     mcp.ChannelTarget{Delivery: mcp.ChannelDeliveryDirect, Address: "person-" + entryID},
	}
}

// --- Notify -------------------------------------------------------------

func TestNotify_AllEntriesSucceed(t *testing.T) {
	clientA := healthyNotifyClient()
	clientB := healthyNotifyClient()
	resolver := notifyMapResolver{"inst-a": clientA, "inst-b": clientB}
	audit := &fakeAuditWriter{}
	notifier := NewNotifier(NotifyConfig{Clients: resolver, Audit: audit})

	// A notify:false entry is not the fan-out's business at all -- it must not
	// even be attempted.
	entries := []Entry{
		notifyEntry("e-a", "inst-a"),
		notifyEntry("e-b", "inst-b"),
		{EntryID: "e-c", InstanceID: "inst-c", Notify: false},
	}

	report := notifier.Notify(context.Background(), "run-1", "input requested", entries)
	if report.Attempted != 2 {
		t.Errorf("Attempted = %d, want 2", report.Attempted)
	}
	if len(report.Failed) != 0 {
		t.Fatalf("Failed = %+v, want none", report.Failed)
	}
	for _, c := range []*fakeNotifyClient{clientA, clientB} {
		got := c.notifications()
		if len(got) != 1 || got[0].Message != "input requested" {
			t.Errorf("notifications = %+v, want one carrying the message", got)
		}
	}
	if audit.count() != 0 {
		t.Errorf("audit events = %d, want 0 on the all-succeed path", audit.count())
	}
}

func TestNotify_OneFailureDoesNotBlockTheOthers(t *testing.T) {
	good1 := healthyNotifyClient()
	good2 := healthyNotifyClient()
	bad := healthyNotifyClient()
	bad.err = errors.New("plugin unreachable")
	resolver := notifyMapResolver{"inst-1": good1, "inst-2": good2, "inst-bad": bad}
	audit := &fakeAuditWriter{}
	notifier := NewNotifier(NotifyConfig{Clients: resolver, Audit: audit})

	entries := []Entry{
		notifyEntry("e-1", "inst-1"),
		notifyEntry("e-2", "inst-2"),
		notifyEntry("e-bad", "inst-bad"),
	}

	report := notifier.Notify(context.Background(), "run-42", "input requested", entries)
	if report.Attempted != 3 {
		t.Errorf("Attempted = %d, want 3", report.Attempted)
	}
	if len(report.Failed) != 1 {
		t.Fatalf("Failed = %+v, want exactly one", report.Failed)
	}
	if report.Failed[0].EntryID != "e-bad" || report.Failed[0].Reason != NotifyFailureRPCError {
		t.Errorf("failure = %+v, want e-bad/notify_rpc_failed", report.Failed[0])
	}
	for _, c := range []*fakeNotifyClient{good1, good2} {
		if len(c.notifications()) != 1 {
			t.Errorf("a sibling failure suppressed delivery to a healthy entry")
		}
	}
	if audit.count() != 1 {
		t.Errorf("audit events = %d, want 1", audit.count())
	}
	// The run_id column is nullable for exactly this: correlating a notify
	// failure back to the run whose HITL request triggered it.
	if got := audit.events[0].RunID; got == nil || *got != "run-42" {
		t.Errorf("audit run_id = %v, want run-42", got)
	}
}

// A notify failure with no run to correlate against (Notify called with "")
// leaves the audit row's run_id NULL rather than writing an empty string.
func TestNotify_NoRunIDLeavesAuditRowUnset(t *testing.T) {
	bad := healthyNotifyClient()
	bad.err = errors.New("plugin unreachable")
	audit := &fakeAuditWriter{}
	notifier := NewNotifier(NotifyConfig{Clients: notifyMapResolver{"inst": bad}, Audit: audit})

	notifier.Notify(context.Background(), "", "input requested", []Entry{notifyEntry("e", "inst")})
	if audit.count() != 1 {
		t.Fatalf("audit events = %d, want 1", audit.count())
	}
	if got := audit.events[0].RunID; got != nil {
		t.Errorf("audit run_id = %v, want nil", *got)
	}
}

func TestNotify_FailureReasons(t *testing.T) {
	tests := []struct {
		name           string
		client         *fakeNotifyClient
		health         fakeCapabilityChecker
		wantReason     NotifyFailureReason
		wantClientCall bool
	}{
		{
			name:           "server does not declare the extension",
			client:         &fakeNotifyClient{declared: false},
			wantReason:     NotifyFailureExtensionNotDeclared,
			wantClientCall: false,
		},
		{
			name: "server does not support the configured delivery",
			client: &fakeNotifyClient{declared: true, capability: mcp.ChannelCapability{
				Version:    mcp.ExtensionChannelVersion,
				Assurance:  mcp.ChannelAssuranceAuthenticated,
				Deliveries: []mcp.ChannelDelivery{mcp.ChannelDeliveryShared},
			}},
			wantReason:     NotifyFailureDeliveryUnsupported,
			wantClientCall: false,
		},
		{
			name: "server declares a major version this host cannot read",
			client: &fakeNotifyClient{declared: true, capability: mcp.ChannelCapability{
				Version:    "2.0.0",
				Assurance:  mcp.ChannelAssuranceAuthenticated,
				Deliveries: []mcp.ChannelDelivery{mcp.ChannelDeliveryDirect},
			}},
			wantReason:     NotifyFailureVersionUnsupported,
			wantClientCall: false,
		},
		{
			name:           "caphealth is not serving human_channel for this instance",
			client:         healthyNotifyClient(),
			health:         fakeCapabilityChecker{"inst": false},
			wantReason:     NotifyFailureCapabilityUnhealthy,
			wantClientCall: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolver := notifyMapResolver{"inst": tc.client}
			audit := &fakeAuditWriter{}
			cfg := NotifyConfig{Clients: resolver, Audit: audit}
			if tc.health != nil {
				cfg.Health = tc.health
			}
			notifier := NewNotifier(cfg)

			report := notifier.Notify(context.Background(), "run-1", "input requested", []Entry{notifyEntry("e", "inst")})
			if len(report.Failed) != 1 {
				t.Fatalf("Failed = %+v, want exactly one", report.Failed)
			}
			if report.Failed[0].Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", report.Failed[0].Reason, tc.wantReason)
			}
			if tc.client.wasCalled() != tc.wantClientCall {
				t.Errorf("ChannelNotify called = %v, want %v", tc.client.wasCalled(), tc.wantClientCall)
			}
			if audit.count() != 1 {
				t.Errorf("audit events = %d, want 1", audit.count())
			}
		})
	}
}

// The shared timeout, not the misbehaving plugin, decides how long Notify
// takes: a client that never returns must not be able to hang the fan-out.
// Uses a blocking fake plus a ctx deadline rather than a sleep (CLAUDE.md
// "Testing time-dependent code").
func TestNotify_TimeoutBoundsWallClock(t *testing.T) {
	blocked := &fakeNotifyClient{
		declared: true,
		capability: mcp.ChannelCapability{
			Version:    mcp.ExtensionChannelVersion,
			Assurance:  mcp.ChannelAssuranceAuthenticated,
			Deliveries: []mcp.ChannelDelivery{mcp.ChannelDeliveryDirect},
		},
		block: true,
	}
	resolver := notifyMapResolver{"inst": blocked}
	notifier := NewNotifier(NotifyConfig{Clients: resolver, Timeout: 20 * time.Millisecond})

	start := time.Now()
	report := notifier.Notify(context.Background(), "run-1", "input requested", []Entry{notifyEntry("e", "inst")})
	elapsed := time.Since(start)

	// Generous CI bound (well over 5x the 20ms timeout) -- the point is that
	// Notify returns at all, not that it returns in exactly 20ms.
	if elapsed > 2*time.Second {
		t.Fatalf("Notify took %s, want it bounded by the configured timeout", elapsed)
	}
	if len(report.Failed) != 1 || report.Failed[0].Reason != NotifyFailureRPCError {
		t.Errorf("report = %+v, want one notify_rpc_failed entry from the timed-out call", report)
	}
}

func TestNotify_InAppEntryIsNeverNotified(t *testing.T) {
	notifier := NewNotifier(NotifyConfig{Clients: notifyMapResolver{}})
	entries := []Entry{{EntryID: "gleipnir.in-app", InApp: true, Notify: true}}

	report := notifier.Notify(context.Background(), "run-1", "input requested", entries)
	if report.Attempted != 0 || len(report.Failed) != 0 {
		t.Errorf("report = %+v, want the in-app entry skipped entirely", report)
	}
}

// --- NotifyRequestRouted ------------------------------------------------

func TestNotifyRequestRouted_MessageNeverCarriesRequestContent(t *testing.T) {
	client := healthyNotifyClient()
	notifier := NewNotifier(NotifyConfig{Clients: notifyMapResolver{"inst": client}})

	run := RunRef{RunID: "run-123", PolicyName: "Prod Deploy", PublicURL: "https://gleipnir.example.com/"}
	report := NotifyRequestRouted(context.Background(), notifier, []Entry{notifyEntry("e", "inst")}, run)
	if len(report.Failed) != 0 {
		t.Fatalf("unexpected failures: %+v", report.Failed)
	}

	got := client.notifications()
	if len(got) != 1 {
		t.Fatalf("got %d notifications, want 1", len(got))
	}
	message := got[0].Message

	// What a real caller's ask would have carried -- none of it may appear.
	for _, forbidden := range []string{"Approve the production deploy", "which region", "requestedSchema"} {
		if strings.Contains(strings.ToLower(message), strings.ToLower(forbidden)) {
			t.Errorf("message %q leaked request content %q", message, forbidden)
		}
	}

	for _, want := range []string{"run-123", "Prod Deploy", "https://gleipnir.example.com/runs/run-123"} {
		if !strings.Contains(message, want) {
			t.Errorf("message %q missing %q", message, want)
		}
	}
}

func TestNotifyRequestRouted_NoPublicURLOmitsLink(t *testing.T) {
	client := healthyNotifyClient()
	notifier := NewNotifier(NotifyConfig{Clients: notifyMapResolver{"inst": client}})

	run := RunRef{RunID: "run-9"}
	NotifyRequestRouted(context.Background(), notifier, []Entry{notifyEntry("e", "inst")}, run)

	got := client.notifications()
	if len(got) != 1 {
		t.Fatalf("got %d notifications, want 1", len(got))
	}
	if strings.Contains(got[0].Message, "http") {
		t.Errorf("message %q carries a link despite no configured public URL", got[0].Message)
	}
	if !strings.Contains(got[0].Message, "run-9") {
		t.Errorf("message %q does not name the run", got[0].Message)
	}
}

func TestNotifyRequestRouted_NilNotifierIsSafe(t *testing.T) {
	report := NotifyRequestRouted(context.Background(), nil, []Entry{notifyEntry("e", "inst")}, RunRef{RunID: "run-1"})
	if report.Attempted != 0 || len(report.Failed) != 0 {
		t.Errorf("report = %+v, want a no-op", report)
	}
}

// A policy name is operator-authored, not attacker-controlled, but it still
// rides into a third-party chat channel -- control characters (which could
// inject extra lines/formatting) must not survive, and it must not be used to
// send an unbounded amount of text into a plugin.
func TestNotifyRequestRouted_SanitizesPolicyName(t *testing.T) {
	client := healthyNotifyClient()
	notifier := NewNotifier(NotifyConfig{Clients: notifyMapResolver{"inst": client}})

	run := RunRef{RunID: "run-7", PolicyName: "Prod\r\nDeploy\x00\tExtra"}
	NotifyRequestRouted(context.Background(), notifier, []Entry{notifyEntry("e", "inst")}, run)

	got := client.notifications()
	if len(got) != 1 {
		t.Fatalf("got %d notifications, want 1", len(got))
	}
	message := got[0].Message
	for _, forbidden := range []string{"\r", "\n", "\x00"} {
		if strings.Contains(message, forbidden) {
			t.Errorf("message %q retains a control character %q", message, forbidden)
		}
	}
	if !strings.Contains(message, "ProdDeployExtra") {
		t.Errorf("message %q should contain the stripped policy name %q", message, "ProdDeployExtra")
	}
}

func TestSanitizePolicyName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain name", "Prod Deploy", "Prod Deploy"},
		{"strips CRLF", "Prod\r\nDeploy", "ProdDeploy"},
		{"strips NUL and tab", "Prod\x00\tDeploy", "ProdDeploy"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizePolicyName(tc.input); got != tc.want {
				t.Errorf("sanitizePolicyName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}

	t.Run("truncates to maxPolicyNameRunes", func(t *testing.T) {
		long := strings.Repeat("a", maxPolicyNameRunes+50)
		got := sanitizePolicyName(long)
		if runes := []rune(got); len(runes) != maxPolicyNameRunes {
			t.Errorf("len(sanitized) = %d, want %d", len(runes), maxPolicyNameRunes)
		}
	})
}
