package caphealth

import (
	"testing"
	"time"

	"github.com/felag-engineering/gleipnir/internal/model"
)

var (
	toolProvider = Capability{Profile: ProfileToolProvider}
	channel      = Capability{Profile: ProfileHumanChannel}
	eventSource  = Capability{Profile: ProfileEventSource}
	deployTool   = Capability{Profile: ProfileToolProvider, Name: "deploy"}
)

func live() Liveness {
	return Liveness{ContainerHealthy: true, DiscoverOK: true, ObservedAt: time.Now()}
}

func entry(c Capability, s model.PluginHealthState, detail string) Entry {
	return Entry{Capability: c, State: s, Detail: detail}
}

// The defect this package exists to fix: one degraded capability must leave the
// others serving, and the rollup must say "partial" rather than "down".
func TestInstanceHealth_OneDegradedCapabilityDoesNotTakeDownTheRest(t *testing.T) {
	h := InstanceHealth{
		InstanceID: "i1",
		Liveness:   live(),
		Entries: []Entry{
			entry(toolProvider, model.PluginHealthStateHealthy, ""),
			entry(channel, model.PluginHealthStateHealthy, ""),
			entry(deployTool, model.PluginHealthStateUnhealthy, "missing scope repo:write"),
		},
	}

	if !h.Serves(toolProvider) {
		t.Error("the tool_provider profile stopped serving because one tool under it is degraded")
	}
	if !h.Serves(channel) {
		t.Error("an unrelated capability stopped serving")
	}
	if h.Serves(deployTool) {
		t.Error("the degraded tool is still being routed to")
	}
	if !h.Partial() {
		t.Error("Partial() = false; the instance is serving some capabilities and not others")
	}
	if got := h.Rollup(); got != model.PluginHealthStateUnhealthy {
		t.Errorf("rollup = %q, want unhealthy — the chip must still show the worst state", got)
	}
	// And it must name what is actually broken.
	if got := h.RollupDetail(); got != "tool_provider/deploy: missing scope repo:write" {
		t.Errorf("detail = %q, want it to name the failing capability", got)
	}
}

// A liveness fault is total: nothing this instance previously claimed can be
// trusted once it stops answering.
func TestInstanceHealth_LivenessFaultOverridesCapabilities(t *testing.T) {
	tests := []struct {
		name     string
		liveness Liveness
	}{
		{
			name:     "container healthcheck failing",
			liveness: Liveness{ContainerHealthy: false, DiscoverOK: true, ObservedAt: time.Now(), Detail: "container healthcheck failing"},
		},
		{
			// A container that is up with a wedged server passes the first
			// check and fails the second; routing there would hang.
			name:     "server not discovering",
			liveness: Liveness{ContainerHealthy: true, DiscoverOK: false, ObservedAt: time.Now(), Detail: "server/discover probe failed"},
		},
		{
			name:     "never observed",
			liveness: Liveness{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := InstanceHealth{
				InstanceID: "i1",
				Liveness:   tc.liveness,
				Entries: []Entry{
					entry(toolProvider, model.PluginHealthStateHealthy, ""),
					entry(channel, model.PluginHealthStateHealthy, ""),
				},
			}

			if got := h.Rollup(); got != model.PluginHealthStateUnhealthy {
				t.Errorf("rollup = %q, want unhealthy", got)
			}
			if h.Serves(toolProvider) || h.Serves(channel) {
				t.Error("a capability still serves despite a liveness fault")
			}
			if h.Partial() {
				t.Error("Partial() = true for a liveness fault; that failure is total, not partial")
			}
			if h.RollupDetail() == "" {
				t.Error("a liveness fault produced no explanation")
			}
		})
	}
}

// Everything healthy rolls up healthy and serves everything.
func TestInstanceHealth_AllHealthy(t *testing.T) {
	h := InstanceHealth{
		InstanceID: "i1",
		Liveness:   live(),
		Entries: []Entry{
			entry(toolProvider, model.PluginHealthStateHealthy, ""),
			entry(channel, model.PluginHealthStateHealthy, ""),
		},
	}

	if got := h.Rollup(); got != model.PluginHealthStateHealthy {
		t.Errorf("rollup = %q, want healthy", got)
	}
	if got := h.RollupDetail(); got != "" {
		t.Errorf("detail = %q, want empty when nothing is wrong", got)
	}
	if h.Partial() {
		t.Error("Partial() = true with nothing degraded")
	}
	if !h.Serves(toolProvider) || !h.Serves(channel) {
		t.Error("a healthy capability is not serving")
	}
}

// Routing questions are answered per capability. This is the table the audience
// dispatcher and the event listener both consult.
func TestInstanceHealth_Serves(t *testing.T) {
	tests := []struct {
		name    string
		entries []Entry
		ask     Capability
		want    bool
	}{
		{
			name:    "healthy serves",
			entries: []Entry{entry(channel, model.PluginHealthStateHealthy, "")},
			ask:     channel,
			want:    true,
		},
		{
			// The operator's deliberate escape hatch, already surfaced as a
			// yellow chip. Refusing to route would silently undo it.
			name:    "unsigned_permissive serves",
			entries: []Entry{entry(channel, model.PluginHealthStateUnsignedPermissive, "")},
			ask:     channel,
			want:    true,
		},
		{
			name:    "unhealthy does not serve",
			entries: []Entry{entry(channel, model.PluginHealthStateUnhealthy, "")},
			ask:     channel,
		},
		{
			name:    "circuit_broken does not serve",
			entries: []Entry{entry(channel, model.PluginHealthStateCircuitBroken, "")},
			ask:     channel,
		},
		{
			// Silence is not a fault. A plugin is not required to report health
			// for every surface, and treating "no signal" as "bad signal" would
			// put every quiet plugin permanently out of service.
			name:    "unknown capability serves",
			entries: nil,
			ask:     channel,
			want:    true,
		},
		{
			// A profile-wide fault covers everything under it.
			name:    "named capability inherits a profile-wide fault",
			entries: []Entry{entry(toolProvider, model.PluginHealthStateUnhealthy, "")},
			ask:     deployTool,
		},
		{
			// ...but a named entry is the more specific answer and wins.
			name: "named entry overrides the profile",
			entries: []Entry{
				entry(toolProvider, model.PluginHealthStateUnhealthy, ""),
				entry(deployTool, model.PluginHealthStateHealthy, ""),
			},
			ask:  deployTool,
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := InstanceHealth{InstanceID: "i1", Liveness: live(), Entries: tc.entries}
			if got := h.Serves(tc.ask); got != tc.want {
				t.Errorf("Serves(%s) = %v, want %v", tc.ask, got, tc.want)
			}
		})
	}
}

// A prober that stops is indistinguishable from one that keeps finding
// everything healthy — unless the observation ages out.
func TestRegistry_StaleLivenessDegrades(t *testing.T) {
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	now := base
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = func() time.Time { return time.Now() } })

	r := NewRegistryWithStaleAfter(time.Minute)
	r.SetLiveness("i1", Liveness{ContainerHealthy: true, DiscoverOK: true})
	r.SetCapability("i1", entry(channel, model.PluginHealthStateHealthy, ""))

	if got := r.Get("i1").Rollup(); got != model.PluginHealthStateHealthy {
		t.Fatalf("rollup = %q immediately after a probe, want healthy", got)
	}
	if !r.Serves("i1", channel) {
		t.Fatal("a freshly probed healthy capability is not serving")
	}

	// The loop stops. Nothing else changes.
	now = base.Add(10 * time.Minute)

	health := r.Get("i1")
	if !health.Stale {
		t.Error("a ten-minute-old verdict is not reported stale")
	}
	if got := health.Rollup(); got != model.PluginHealthStateUnhealthy {
		t.Errorf("rollup = %q for a stale verdict, want unhealthy", got)
	}
	if r.Serves("i1", channel) {
		t.Error("a stale verdict is still certifying a capability as routable")
	}
	if health.RollupDetail() == "" {
		t.Error("a stale verdict produced no explanation")
	}
}

// A registry with no staleness window never ages out — for callers that drive
// it synchronously and have no loop to lose.
func TestRegistry_NoStalenessWindow(t *testing.T) {
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	now := base
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = func() time.Time { return time.Now() } })

	r := NewRegistry()
	r.SetLiveness("i1", Liveness{ContainerHealthy: true, DiscoverOK: true})

	now = base.Add(100 * time.Hour)
	if r.Get("i1").Stale {
		t.Error("a registry with no staleness window aged a verdict out")
	}
}

// An instance nothing has probed reports not-live, which is the honest answer:
// the host does not know, and claiming reachable would route at a guess.
func TestRegistry_UnprobedInstanceIsNotLive(t *testing.T) {
	r := NewRegistry()
	h := r.Get("never-seen")

	if h.Liveness.OK() {
		t.Error("an unprobed instance reports live")
	}
	if got := h.Rollup(); got != model.PluginHealthStateUnhealthy {
		t.Errorf("rollup = %q, want unhealthy", got)
	}
	if r.Serves("never-seen", channel) {
		t.Error("an unprobed instance is being routed to")
	}
}

func TestRegistry_SetReplacesAndClearRemoves(t *testing.T) {
	r := NewRegistry()
	r.SetLiveness("i1", live())

	r.SetCapability("i1", entry(channel, model.PluginHealthStateUnhealthy, "first"))
	r.SetCapability("i1", entry(channel, model.PluginHealthStateUnhealthy, "second"))

	h := r.Get("i1")
	if len(h.Entries) != 1 {
		t.Fatalf("%d entries after two reports for one capability, want 1", len(h.Entries))
	}
	if h.Entries[0].Detail != "second" {
		t.Errorf("detail = %q, want the later report to replace the earlier", h.Entries[0].Detail)
	}

	r.ClearCapability("i1", channel)
	if got := len(r.Get("i1").Entries); got != 0 {
		t.Errorf("%d entries after clear, want 0", got)
	}
}

func TestRegistry_Forget(t *testing.T) {
	r := NewRegistry()
	r.SetLiveness("i1", live())
	r.SetCapability("i1", entry(channel, model.PluginHealthStateHealthy, ""))

	r.Forget("i1")

	h := r.Get("i1")
	if h.Liveness.OK() || len(h.Entries) != 0 {
		t.Errorf("Forget left state behind: %+v", h)
	}
}

// Entries come back in a stable order so an API response and a test assertion
// both get the same list every time.
func TestRegistry_EntriesAreSorted(t *testing.T) {
	r := NewRegistry()
	r.SetLiveness("i1", live())
	r.SetCapability("i1", entry(eventSource, model.PluginHealthStateHealthy, ""))
	r.SetCapability("i1", entry(channel, model.PluginHealthStateHealthy, ""))
	r.SetCapability("i1", entry(toolProvider, model.PluginHealthStateHealthy, ""))

	var got []string
	for _, e := range r.Get("i1").Entries {
		got = append(got, e.Capability.String())
	}
	want := []string{"event_source", "human_channel", "tool_provider"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entries = %v, want %v", got, want)
		}
	}
}

// Drift is reported in both directions, and the two mean different things.
func TestDriftDetail(t *testing.T) {
	tests := []struct {
		name       string
		attested   []string
		discovered []string
		wantEmpty  bool
		contains   []string
	}{
		{
			name:       "in agreement",
			attested:   []string{"a", "b"},
			discovered: []string{"b", "a"},
			wantEmpty:  true,
		},
		{
			// A capability an operator approved and is not getting.
			name:       "attested but not discovered",
			attested:   []string{"a", "b"},
			discovered: []string{"a"},
			contains:   []string{"attested but not discovered", "b"},
		},
		{
			// The more serious direction: a surface nobody signed for.
			name:       "discovered but not attested",
			attested:   []string{"a"},
			discovered: []string{"a", "rogue"},
			contains:   []string{"discovered but not attested", "rogue"},
		},
		{
			name:       "both directions",
			attested:   []string{"a", "b"},
			discovered: []string{"a", "rogue"},
			contains:   []string{"attested but not discovered", "b", "discovered but not attested", "rogue"},
		},
		{
			name:       "nothing on either side",
			wantEmpty:  true,
			attested:   nil,
			discovered: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DriftDetail(tc.attested, tc.discovered)
			if tc.wantEmpty {
				if got != "" {
					t.Errorf("DriftDetail = %q, want empty", got)
				}
				return
			}
			for _, want := range tc.contains {
				if !contains(got, want) {
					t.Errorf("DriftDetail = %q, want it to mention %q", got, want)
				}
			}
		})
	}
}

func TestCapabilityString(t *testing.T) {
	if got := toolProvider.String(); got != "tool_provider" {
		t.Errorf("String() = %q", got)
	}
	if got := deployTool.String(); got != "tool_provider/deploy" {
		t.Errorf("String() = %q", got)
	}
}

func TestProfileValid(t *testing.T) {
	for _, p := range []Profile{ProfileToolProvider, ProfileEventSource, ProfileHumanChannel, ProfileIdentityProvider} {
		if !p.Valid() {
			t.Errorf("Profile(%q).Valid() = false", p)
		}
	}
	for _, p := range []Profile{"", "tools", "ToolProvider"} {
		if p.Valid() {
			t.Errorf("Profile(%q).Valid() = true", p)
		}
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
