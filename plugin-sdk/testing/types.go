package testing

import (
	"log/slog"
	"time"

	hostv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/host/v1"
)

// Metric is a projected record of an EmitMetric call received by the fake host.
type Metric struct {
	Name   string
	Value  float64
	Labels map[string]string
}

// Event is a projected record of an EmitEvent call received by the fake host.
type Event struct {
	EventID     string
	EventKind   string
	PayloadJSON string
}

// LogLine is a projected record of a Log call received by the fake host.
type LogLine struct {
	Level slog.Level
	Msg   string
	Attrs map[string]string
}

// RunContext is the run context provided to the fake host via WithRunContext.
type RunContext struct {
	RunID    string
	PolicyID string
	// PluginID is reserved for a future proto version of GetRunContextResponse
	// that will carry a plugin_id field. It has no effect on RPC responses today
	// and is silently ignored by the fake host.
	PluginID string
	// InstanceID is reserved for a future proto version of GetRunContextResponse
	// that will carry an instance_id field. It has no effect on RPC responses today
	// and is silently ignored by the fake host.
	InstanceID string
	StartedAt  time.Time
}

// RunSummary mirrors the fields of hostv1.RunSummary without exposing proto types.
type RunSummary struct {
	RunID      string
	PolicyID   string
	Status     string
	StartedAt  string
	FinishedAt string
}

// UserEntry mirrors the fields of hostv1.UserEntry without exposing proto types.
type UserEntry struct {
	UserID   string
	Username string
	Role     string
}

// HealthState represents the plugin health state reported via SetHealthState.
// Values match the wire numbers of hostv1.PluginHealthState so the internal
// projection is a numeric cast.
type HealthState int32

const (
	HealthStateUnspecified HealthState = 0
	HealthStateHealthy     HealthState = 1
	HealthStateUnavailable HealthState = 2
	HealthStateUnhealthy   HealthState = 3
)

// ── package-private converters ───────────────────────────────────────────────

func fromProtoMetric(req *hostv1.EmitMetricRequest) Metric {
	labels := make(map[string]string, len(req.GetLabels()))
	for k, v := range req.GetLabels() {
		labels[k] = v
	}
	return Metric{
		Name:   req.GetName(),
		Value:  req.GetValue(),
		Labels: labels,
	}
}

func fromProtoEvent(req *hostv1.EmitEventRequest) Event {
	return Event{
		EventID:     req.GetEventId(),
		EventKind:   req.GetEventKind(),
		PayloadJSON: req.GetPayloadJson(),
	}
}

func fromProtoLog(req *hostv1.LogRequest) LogLine {
	attrs := make(map[string]string, len(req.GetAttrs()))
	for k, v := range req.GetAttrs() {
		attrs[k] = v
	}
	return LogLine{
		Level: slogLevelFromProto(req.GetLevel()),
		Msg:   req.GetMsg(),
		Attrs: attrs,
	}
}

func fromProtoHealth(req *hostv1.SetHealthStateRequest) (HealthState, string) {
	return HealthState(req.GetState()), req.GetDetail()
}

func toProtoRunSummary(r RunSummary) *hostv1.RunSummary {
	return &hostv1.RunSummary{
		RunId:      r.RunID,
		PolicyId:   r.PolicyID,
		Status:     r.Status,
		StartedAt:  r.StartedAt,
		FinishedAt: r.FinishedAt,
	}
}

func toProtoUserEntry(u UserEntry) *hostv1.UserEntry {
	return &hostv1.UserEntry{
		UserId:   u.UserID,
		Username: u.Username,
		Role:     u.Role,
	}
}

// slogLevelFromProto converts a hostv1.LogLevel to the corresponding slog.Level.
// Re-implemented locally so the internal package does not need to export it.
func slogLevelFromProto(l hostv1.LogLevel) slog.Level {
	switch l {
	case hostv1.LogLevel_LOG_LEVEL_DEBUG:
		return slog.LevelDebug
	case hostv1.LogLevel_LOG_LEVEL_INFO:
		return slog.LevelInfo
	case hostv1.LogLevel_LOG_LEVEL_WARN:
		return slog.LevelWarn
	case hostv1.LogLevel_LOG_LEVEL_ERROR:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
