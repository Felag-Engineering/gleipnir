package hostclienttest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/felag-engineering/gleipnir/plugin-sdk/hostclient"
)

// Host tool names (contract §6), duplicated from hostendpoint.ToolNames()
// for the same not-importing-internal reason as everywhere else in this
// package (doc.go). toolNames is the inventory tools/list reports, in the
// same stable order the real endpoint uses.
const (
	toolGetInstanceConfig   = "host/get_instance_config"
	toolGetCredentials      = "host/get_credentials"
	toolGetRunContext       = "host/get_run_context"
	toolEmitMetric          = "host/emit_metric"
	toolLog                 = "host/log"
	toolSetHealthState      = "host/set_health_state"
	toolRunHistoryRead      = "host/run_history_read"
	toolUserDirectoryRead   = "host/user_directory_read"
	toolAuthorizeActor      = "host/authorize_actor"
	toolSubmitIdentityProof = "host/submit_identity_proof"
	toolGetUserConfig       = "host/get_user_config"
)

var toolNames = []string{
	toolGetInstanceConfig,
	toolGetCredentials,
	toolGetRunContext,
	toolEmitMetric,
	toolLog,
	toolSetHealthState,
	toolRunHistoryRead,
	toolUserDirectoryRead,
	toolAuthorizeActor,
	toolSubmitIdentityProof,
	toolGetUserConfig,
}

// Log caps, carried over from the real endpoint unchanged (contract §6
// host/log): 4 KiB for msg, 32 attrs, 256 bytes per key/value.
const (
	maxLogMsgBytes  = 4 * 1024
	maxLogAttrs     = 32
	maxLogAttrBytes = 256
)

func (s *Server) getInstanceConfig(_ *http.Request, _ json.RawMessage) (any, error) {
	s.mu.Lock()
	cfg := s.instanceConfigJSON
	s.mu.Unlock()
	return map[string]any{"config_json": cfg}, nil
}

func (s *Server) getCredentials(_ *http.Request, _ json.RawMessage) (any, error) {
	s.mu.Lock()
	creds := s.credentialsJSON
	s.mu.Unlock()
	// Empty credentials is a valid state, not an error — matches
	// hostendpoint.getCredentials's "no credentials configured" case.
	return map[string]any{"credentials_json": creds}, nil
}

// getRunContext resolves the call identified by the incoming
// Gleipnir-Call-Id header against WithRunContext's configured table.
// Requires a header (failed_precondition if absent), and the header must
// resolve to a configured entry (failed_precondition if not) — the same two
// failure shapes the real endpoint reports for "no call id" and "call id not
// in flight" (contract §6 host/get_run_context).
func (s *Server) getRunContext(r *http.Request, _ json.RawMessage) (any, error) {
	callID, ok := callIDFromContext(r.Context())
	if !ok {
		return nil, &toolError{Code: "failed_precondition", Message: "host/get_run_context requires a Gleipnir-Call-Id header"}
	}
	s.mu.Lock()
	rc, ok := s.runContexts[callID]
	s.mu.Unlock()
	if !ok {
		return nil, &toolError{Code: "failed_precondition", Message: fmt.Sprintf("call_id %q is not currently in-flight", callID)}
	}
	return map[string]any{
		"run_id":     rc.RunID,
		"policy_id":  rc.PolicyID,
		"started_at": rc.StartedAt,
		"step_index": rc.StepIndex,
	}, nil
}

type emitMetricArgs struct {
	Name   string            `json:"name"`
	Value  float64           `json:"value"`
	Labels map[string]string `json:"labels"`
}

// maxMetricNameBytes mirrors pluginmetrics.MaxMetricNameBytes (the ADR-047
// guard's own cap, not duplicated in full here — see doc.go's divergence
// note on the cardinality cap).
const maxMetricNameBytes = 128

// metricNamePrefix is the prefix the host adds automatically; a plugin
// supplying it itself is the same invalid_metric_name rejection the real
// endpoint reports (pluginmetrics.go).
const metricNamePrefix = "gleipnir_plugin_"

func (s *Server) emitMetric(_ *http.Request, args json.RawMessage) (any, error) {
	var a emitMetricArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, &toolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
	}
	// Name validation mirrors pluginmetrics.Metrics.Set exactly: all three
	// cases are invalid_metric_name, not invalid_argument — the real server's
	// error code, and the one a plugin author's error-handling code branches
	// on.
	switch {
	case strings.HasPrefix(a.Name, metricNamePrefix):
		return nil, &toolError{Code: "invalid_metric_name", Message: "metric name must not include the gleipnir_plugin_ prefix (host adds it automatically)"}
	case a.Name == "":
		return nil, &toolError{Code: "invalid_metric_name", Message: "metric name must not be empty"}
	case len(a.Name) > maxMetricNameBytes:
		return nil, &toolError{Code: "invalid_metric_name", Message: fmt.Sprintf("metric name exceeds maximum length of %d bytes", maxMetricNameBytes)}
	}
	s.mu.Lock()
	s.metrics = append(s.metrics, MetricEntry{Name: a.Name, Value: a.Value, Labels: a.Labels})
	s.mu.Unlock()
	return map[string]any{"ok": true}, nil
}

type logArgs struct {
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs"`
}

func (s *Server) log(_ *http.Request, args json.RawMessage) (any, error) {
	var a logArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, &toolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
	}
	if len(a.Msg) > maxLogMsgBytes {
		return nil, &toolError{Code: "invalid_argument", Message: fmt.Sprintf("msg exceeds maximum size of %d bytes", maxLogMsgBytes)}
	}
	if len(a.Attrs) > maxLogAttrs {
		return nil, &toolError{Code: "invalid_argument", Message: fmt.Sprintf("attrs map exceeds maximum of %d entries", maxLogAttrs)}
	}
	for k, v := range a.Attrs {
		if len(k) > maxLogAttrBytes || len(v) > maxLogAttrBytes {
			return nil, &toolError{Code: "invalid_argument", Message: fmt.Sprintf("attr key/value exceeds maximum size of %d bytes", maxLogAttrBytes)}
		}
	}
	s.mu.Lock()
	s.logs = append(s.logs, LogEntry{Level: a.Level, Msg: a.Msg, Attrs: a.Attrs})
	s.mu.Unlock()
	return map[string]any{"ok": true}, nil
}

type setHealthStateArgs struct {
	Profile    string `json:"profile"`
	Capability string `json:"capability"`
	State      string `json:"state"`
	Detail     string `json:"detail"`
}

var validHealthProfiles = map[string]bool{
	hostclient.ProfileToolProvider:     true,
	hostclient.ProfileEventSource:      true,
	hostclient.ProfileHumanChannel:     true,
	hostclient.ProfileIdentityProvider: true,
}

// setHealthState records a per-capability self-report, applying the §8.1
// "plugin can only mark itself worse" rule: reporting healthy over a
// capability this fake last recorded as unhealthy is a no-op
// ({ok:true, applied:false}) — recovery is the host's observation to make,
// mirroring hostendpoint's caphealth.Registry.SelfReportCapability.
func (s *Server) setHealthState(_ *http.Request, args json.RawMessage) (any, error) {
	var a setHealthStateArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, &toolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
	}
	if !validHealthProfiles[a.Profile] {
		return nil, &toolError{Code: "invalid_argument", Message: fmt.Sprintf("unknown capability profile %q", a.Profile)}
	}
	switch a.State {
	case hostclient.HealthStateHealthy, hostclient.HealthStateUnhealthy:
	default:
		return nil, &toolError{Code: "invalid_argument", Message: fmt.Sprintf("state %q is not plugin-reportable (healthy|unhealthy)", a.State)}
	}

	key := a.Profile + "\x00" + a.Capability
	s.mu.Lock()
	prev, hadPrev := s.healthState[key]
	applied := true
	if hadPrev && prev == hostclient.HealthStateUnhealthy && a.State == hostclient.HealthStateHealthy {
		applied = false
	} else {
		s.healthState[key] = a.State
	}
	s.health = append(s.health, HealthEntry{Profile: a.Profile, Capability: a.Capability, State: a.State, Detail: a.Detail, Applied: applied})
	s.mu.Unlock()

	return map[string]any{"ok": true, "applied": applied}, nil
}

type runHistoryReadArgs struct {
	PolicyID string `json:"policy_id"`
	Limit    int64  `json:"limit"`
}

// runHistoryRead serves whatever WithRunHistory configured, optionally
// narrowed to one policy id. Unlike the real endpoint, this fake enforces no
// Tier-2 manifest-capability gate — see doc.go's divergence note.
func (s *Server) runHistoryRead(_ *http.Request, args json.RawMessage) (any, error) {
	var a runHistoryReadArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, &toolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
		}
	}
	s.mu.Lock()
	runs := append([]RunSummary(nil), s.runHistory...)
	s.mu.Unlock()

	if a.PolicyID != "" {
		filtered := make([]RunSummary, 0, len(runs))
		for _, run := range runs {
			if run.PolicyID == a.PolicyID {
				filtered = append(filtered, run)
			}
		}
		runs = filtered
	}
	limit := a.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if int64(len(runs)) > limit {
		runs = runs[:limit]
	}
	if runs == nil {
		runs = []RunSummary{}
	}
	return map[string]any{"runs": runs}, nil
}

type userDirectoryReadArgs struct {
	RoleFilter string `json:"role_filter"`
}

var validUserRoles = map[string]bool{
	"admin": true, "operator": true, "approver": true, "auditor": true,
}

// userDirectoryRead serves whatever WithUserDirectory configured, optionally
// narrowed to one role. Same no-Tier-2-gate divergence as runHistoryRead.
func (s *Server) userDirectoryRead(_ *http.Request, args json.RawMessage) (any, error) {
	var a userDirectoryReadArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, &toolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
		}
	}
	if a.RoleFilter != "" && !validUserRoles[a.RoleFilter] {
		return nil, &toolError{Code: "invalid_argument", Message: fmt.Sprintf("unknown role %q", a.RoleFilter)}
	}
	s.mu.Lock()
	users := append([]UserEntry(nil), s.userDirectory...)
	s.mu.Unlock()

	if a.RoleFilter != "" {
		filtered := make([]UserEntry, 0, len(users))
		for _, u := range users {
			if u.Role == a.RoleFilter {
				filtered = append(filtered, u)
			}
		}
		users = filtered
	}
	if users == nil {
		users = []UserEntry{}
	}
	return map[string]any{"users": users}, nil
}

type authorizeActorArgs struct {
	RequestID       string `json:"request_id"`
	ActorExternalID string `json:"actor_external_id"`
}

// authorizeActorCall runs the configured ActorAuthorizer (WithAuthorizePolicy,
// or the default WithAuthorizedActor set-membership check). An unauthorized
// actor is a non-error result — {"authorized": false} — never a ToolError,
// matching the real endpoint exactly (contract §6 host/authorize_actor: the
// request stays open for a legitimately authorized actor to try again).
func (s *Server) authorizeActorCall(_ *http.Request, args json.RawMessage) (any, error) {
	var a authorizeActorArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, &toolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
	}
	if a.RequestID == "" {
		return nil, &toolError{Code: "invalid_argument", Message: "request_id is required"}
	}
	if a.ActorExternalID == "" {
		return nil, &toolError{Code: "invalid_argument", Message: "actor_external_id is required"}
	}

	s.mu.Lock()
	policy := s.authorizePolicy
	actors := s.authorizedActors
	s.mu.Unlock()

	var authorized bool
	var userID string
	if policy != nil {
		authorized, userID = policy(a.RequestID, a.ActorExternalID)
	} else {
		userID, authorized = actors[a.ActorExternalID]
	}
	if !authorized {
		return map[string]any{"authorized": false}, nil
	}
	return map[string]any{"authorized": true, "user_id": userID}, nil
}

type submitIdentityProofArgs struct {
	ExternalUserID string `json:"external_user_id"`
	Code           string `json:"code"`
}

// submitIdentityProof runs the configured IdentityBinder (WithIdentityBinder),
// or rejects with ReasonNoPendingLink by default — mirroring a real host with
// no PendingLinkBinder configured. The result is always a BindResult, never a
// ToolError: a rejected proof is a clean outcome for the plugin to relay, not
// a transport fault (contract §7.1).
func (s *Server) submitIdentityProof(_ *http.Request, args json.RawMessage) (any, error) {
	var a submitIdentityProofArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, &toolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
	}
	if a.ExternalUserID == "" || a.Code == "" {
		return nil, &toolError{Code: "invalid_argument", Message: "external_user_id and code are both required"}
	}

	s.mu.Lock()
	binder := s.binder
	s.mu.Unlock()
	if binder == nil {
		return BindResult{Accepted: false, Reason: ReasonNoPendingLink}, nil
	}
	accepted, reason := binder(a.ExternalUserID, a.Code)
	return BindResult{Accepted: accepted, Reason: reason}, nil
}

type getUserConfigArgs struct {
	ExternalUserID string `json:"external_user_id"`
}

// getUserConfig serves whatever WithUserConfig configured for the requested
// external user id, defaulting to "{}" — a usable default, never an error —
// for a user with no preferences configured (contract §6
// host/get_user_config).
func (s *Server) getUserConfig(_ *http.Request, args json.RawMessage) (any, error) {
	var a getUserConfigArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, &toolError{Code: "invalid_argument", Message: fmt.Sprintf("decode arguments: %v", err)}
	}
	if a.ExternalUserID == "" {
		return nil, &toolError{Code: "invalid_argument", Message: "external_user_id is required"}
	}
	s.mu.Lock()
	cfg, ok := s.userConfigs[a.ExternalUserID]
	s.mu.Unlock()
	if !ok || cfg == "" {
		cfg = "{}"
	}
	return map[string]any{"user_config_json": cfg}, nil
}
