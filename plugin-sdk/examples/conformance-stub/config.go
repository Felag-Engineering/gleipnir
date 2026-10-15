package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

const (
	profileToolProvider = "tool_provider"
	profileEventSource  = "event_source"
	profileHumanChannel = "human_channel"
)

const (
	autoResolveOff         = "off"
	autoResolveFirstOption = "first_option"
	autoResolveReject      = "reject"
)

// Config is the stub's whole runtime configuration, parsed once at startup.
type Config struct {
	Profiles Profiles

	EmitCount    int
	EmitInterval time.Duration

	Assurance string

	AutoResolve      string
	AutoResolveDelay time.Duration
	ActorID          string

	// DataDir holds the durable state: events/ for the event store, tasks/
	// for the channel task store.
	DataDir string
}

// Profiles is the set of profiles this process serves.
type Profiles struct {
	ToolProvider bool
	EventSource  bool
	HumanChannel bool
}

// Names returns the enabled profiles in manifest declaration order.
func (p Profiles) Names() []string {
	var out []string
	if p.ToolProvider {
		out = append(out, profileToolProvider)
	}
	if p.EventSource {
		out = append(out, profileEventSource)
	}
	if p.HumanChannel {
		out = append(out, profileHumanChannel)
	}
	return out
}

// ParseConfig reads the STUB_* variables through lookup (os.LookupEnv in
// production). Every bad value is reported, not just the first, so one failed
// start tells the operator everything to fix.
func ParseConfig(lookup func(string) (string, bool)) (Config, error) {
	get := func(key, def string) string {
		if v, ok := lookup(key); ok && v != "" {
			return v
		}
		return def
	}

	var errs []error
	fail := func(key string, err error) { errs = append(errs, fmt.Errorf("%s: %w", key, err)) }

	cfg := Config{
		Assurance:   get("STUB_ASSURANCE", manifestv2.AssuranceAuthenticated),
		AutoResolve: get("STUB_AUTO_RESOLVE", autoResolveOff),
		ActorID:     get("STUB_ACTOR_ID", "conformance-actor"),
		DataDir:     get("STUB_DATA_DIR", "/data"),
	}

	profiles, err := parseProfiles(get("STUB_PROFILES", "tool_provider,event_source,human_channel"))
	if err != nil {
		fail("STUB_PROFILES", err)
	}
	cfg.Profiles = profiles

	if cfg.EmitCount, err = parseNonNegativeInt(get("STUB_EMIT_COUNT", "3")); err != nil {
		fail("STUB_EMIT_COUNT", err)
	}
	emitMs, err := parseNonNegativeInt(get("STUB_EMIT_INTERVAL_MS", "1000"))
	if err != nil {
		fail("STUB_EMIT_INTERVAL_MS", err)
	}
	cfg.EmitInterval = time.Duration(emitMs) * time.Millisecond

	switch cfg.Assurance {
	case manifestv2.AssuranceAuthenticated, manifestv2.AssuranceWeak:
	default:
		fail("STUB_ASSURANCE", fmt.Errorf("must be %q or %q, got %q",
			manifestv2.AssuranceAuthenticated, manifestv2.AssuranceWeak, cfg.Assurance))
	}

	switch cfg.AutoResolve {
	case autoResolveOff, autoResolveFirstOption, autoResolveReject:
	default:
		fail("STUB_AUTO_RESOLVE", fmt.Errorf("must be %q, %q or %q, got %q",
			autoResolveOff, autoResolveFirstOption, autoResolveReject, cfg.AutoResolve))
	}
	delayMs, err := parseNonNegativeInt(get("STUB_AUTO_RESOLVE_DELAY_MS", "0"))
	if err != nil {
		fail("STUB_AUTO_RESOLVE_DELAY_MS", err)
	}
	cfg.AutoResolveDelay = time.Duration(delayMs) * time.Millisecond

	if cfg.AutoResolve != autoResolveOff && !cfg.Profiles.HumanChannel {
		fail("STUB_AUTO_RESOLVE", fmt.Errorf("%q needs the %s profile, which STUB_PROFILES does not enable",
			cfg.AutoResolve, profileHumanChannel))
	}

	return cfg, errors.Join(errs...)
}

func parseProfiles(raw string) (Profiles, error) {
	var p Profiles
	seen := map[string]bool{}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if seen[name] {
			return Profiles{}, fmt.Errorf("profile %q listed twice", name)
		}
		seen[name] = true
		switch name {
		case profileToolProvider:
			p.ToolProvider = true
		case profileEventSource:
			p.EventSource = true
		case profileHumanChannel:
			p.HumanChannel = true
		default:
			return Profiles{}, fmt.Errorf("unknown profile %q; want a comma-separated subset of %s, %s, %s",
				name, profileToolProvider, profileEventSource, profileHumanChannel)
		}
	}
	return p, nil
}

func parseNonNegativeInt(raw string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%q is not an integer", raw)
	}
	if n < 0 {
		return 0, fmt.Errorf("must not be negative, got %d", n)
	}
	return n, nil
}
