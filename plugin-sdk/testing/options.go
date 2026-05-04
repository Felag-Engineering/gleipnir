package testing

import (
	"log/slog"

	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/internal/fakehost"
)

// config holds the resolved options for a FakeHost. It is populated by
// applying Option functions in NewFakeHost.
type config struct {
	instanceConfigJSON string
	credentialsJSON    string
	runContext         RunContext
	logger             *slog.Logger
	onEmitEvent        func(Event)
	runHistory         []RunSummary
	userDirectory      []UserEntry
}

// Option is a functional option for NewFakeHost.
type Option func(*config)

// WithInstanceConfigJSON sets the JSON returned by GetInstanceConfig.
// Defaults to "{}".
func WithInstanceConfigJSON(s string) Option {
	return func(c *config) {
		c.instanceConfigJSON = s
	}
}

// WithCredentialsJSON sets the JSON returned by GetCredentials.
// Defaults to "{}".
func WithCredentialsJSON(s string) Option {
	return func(c *config) {
		c.credentialsJSON = s
	}
}

// WithRunContext sets the run context returned by GetRunContext.
// Accepts the local RunContext type — no proto import required.
func WithRunContext(rc RunContext) Option {
	return func(c *config) {
		c.runContext = rc
	}
}

// WithLogger sets the slog.Logger that receives forwarded Log RPCs.
// Defaults to slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		c.logger = l
	}
}

// OnEmitEvent registers a callback invoked synchronously for every EmitEvent
// call, with the event projected to the local Event type. The callback must
// not call back into the FakeHost — it runs after the inner host has released
// its mutex (per the host.go:203–213 guarantee) but a re-entrant host call
// would still cause a deadlock.
func OnEmitEvent(cb func(Event)) Option {
	return func(c *config) {
		c.onEmitEvent = cb
	}
}

// WithRunHistory seeds the Tier-2 RunHistoryRead stub with canned run
// summaries. Without this option RunHistoryRead returns codes.Unimplemented.
func WithRunHistory(runs []RunSummary) Option {
	return func(c *config) {
		c.runHistory = runs
	}
}

// WithUserDirectory seeds the Tier-2 UserDirectoryRead stub with canned user
// entries. Without this option UserDirectoryRead returns codes.Unimplemented.
func WithUserDirectory(users []UserEntry) Option {
	return func(c *config) {
		c.userDirectory = users
	}
}

// ── internal helpers ─────────────────────────────────────────────────────────

// toFakehostOptions converts the resolved config into fakehost.Options.
func toFakehostOptions(cfg *config) fakehost.Options {
	opts := fakehost.Options{
		InstanceConfigJSON: cfg.instanceConfigJSON,
		CredentialsJSON:    cfg.credentialsJSON,
		Logger:             cfg.logger,
		RunContext: fakehost.RunContext{
			RunID:     cfg.runContext.RunID,
			PolicyID:  cfg.runContext.PolicyID,
			StartedAt: cfg.runContext.StartedAt,
		},
	}

	if cfg.onEmitEvent != nil {
		cb := cfg.onEmitEvent
		// The adapter projects the proto request to a local Event and calls the
		// user callback. The callback MUST NOT call back into the FakeHost —
		// doing so would be a logical/contract violation: the emission is still
		// in progress and mutating host state mid-emission leads to undefined
		// behaviour from the caller's perspective.
		opts.OnEmitEvent = func(req *hostv1.EmitEventRequest) {
			cb(fromProtoEvent(req))
		}
	}

	if cfg.runHistory != nil {
		protos := make([]*hostv1.RunSummary, len(cfg.runHistory))
		for i, r := range cfg.runHistory {
			protos[i] = toProtoRunSummary(r)
		}
		opts.RunHistoryRuns = protos
	}

	if cfg.userDirectory != nil {
		protos := make([]*hostv1.UserEntry, len(cfg.userDirectory))
		for i, u := range cfg.userDirectory {
			protos[i] = toProtoUserEntry(u)
		}
		opts.UserDirectoryUsers = protos
	}

	return opts
}
