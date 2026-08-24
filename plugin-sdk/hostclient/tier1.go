package hostclient

import "context"

// Host tool names. Unexported: authors call the typed Client methods below,
// never these strings directly. Values match
// internal/plugin/hostendpoint.ToolNames() exactly (duplicated for the usual
// zero-protobuf/no-internal-import reason).
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

// Plugin-reportable health states for SetHealthStateRequest.State. Exactly
// these two — healthy and unhealthy — mirror the host's enforced vocabulary
// (internal/plugin/hostendpoint's setHealthState): a plugin cannot assert
// circuit_broken or any pending_* state, those are host/admin verdicts.
const (
	HealthStateHealthy   = "healthy"
	HealthStateUnhealthy = "unhealthy"
)

// Log levels for LogRequest.Level. An empty or unrecognized level is treated
// as LogLevelInfo by the host.
const (
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

// Capability profiles for SetHealthStateRequest.Profile, matching
// internal/plugin/caphealth.Profile's declared values. A plugin reports
// health per (profile, capability) — narrowing exactly one surface rather
// than the whole instance (#814).
const (
	ProfileToolProvider     = "tool_provider"
	ProfileEventSource      = "event_source"
	ProfileHumanChannel     = "human_channel"
	ProfileIdentityProvider = "identity_provider"
)

// GetInstanceConfigResponse is host/get_instance_config's result.
type GetInstanceConfigResponse struct {
	ConfigJSON string `json:"config_json"`
}

// GetInstanceConfig returns the calling instance's config_json verbatim.
func (c *Client) GetInstanceConfig(ctx context.Context) (*GetInstanceConfigResponse, error) {
	var out GetInstanceConfigResponse
	if err := c.callTool(ctx, toolGetInstanceConfig, struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetCredentialsResponse is host/get_credentials's result.
type GetCredentialsResponse struct {
	// CredentialsJSON is empty (not an error) when the instance has no
	// credentials configured.
	CredentialsJSON string `json:"credentials_json"`
}

// GetCredentials returns the calling instance's decrypted credentials JSON.
// Exists for the same reason it did on the gRPC plane: header injection
// cannot cover a plugin holding a standing connection to its substrate (a
// websocket, an IMAP session) rather than making individual HTTP calls.
func (c *Client) GetCredentials(ctx context.Context) (*GetCredentialsResponse, error) {
	var out GetCredentialsResponse
	if err := c.callTool(ctx, toolGetCredentials, struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRunContextResponse is host/get_run_context's result.
type GetRunContextResponse struct {
	RunID     string `json:"run_id"`
	PolicyID  string `json:"policy_id"`
	StartedAt string `json:"started_at"`
	StepIndex int64  `json:"step_index"`
}

// GetRunContext resolves run/policy/step context for the in-flight call
// identified by the Gleipnir-Call-Id header. The caller must attach a call
// id to ctx via WithCallID first — without one, the host returns a
// failed_precondition HostError.
func (c *Client) GetRunContext(ctx context.Context) (*GetRunContextResponse, error) {
	var out GetRunContextResponse
	if err := c.callTool(ctx, toolGetRunContext, struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EmitMetricRequest is host/emit_metric's arguments. Name is force-prefixed
// gleipnir_plugin_ and Labels are auto-augmented with plugin/instance by the
// host — do not attempt to set those labels here.
type EmitMetricRequest struct {
	Name   string            `json:"name"`
	Value  float64           `json:"value"`
	Labels map[string]string `json:"labels,omitempty"`
}

// EmitMetricResponse is host/emit_metric's result.
type EmitMetricResponse struct {
	OK bool `json:"ok"`
}

// EmitMetric records a plugin-emitted gauge metric, subject to the host's
// ADR-047 cardinality cap and label-consistency rules.
func (c *Client) EmitMetric(ctx context.Context, req EmitMetricRequest) (*EmitMetricResponse, error) {
	var out EmitMetricResponse
	if err := c.callTool(ctx, toolEmitMetric, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LogRequest is host/log's arguments.
type LogRequest struct {
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// LogResponse is host/log's result.
type LogResponse struct {
	OK bool `json:"ok"`
}

// Log routes a structured line through the host's logging pipeline. Run
// correlation attaches automatically when ctx carries a call id (WithCallID)
// that resolves to an in-flight call; otherwise the line is still recorded,
// just without run/policy/step attribution.
func (c *Client) Log(ctx context.Context, req LogRequest) (*LogResponse, error) {
	var out LogResponse
	if err := c.callTool(ctx, toolLog, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetHealthStateRequest is host/set_health_state's arguments. Capability may
// be empty to report on the profile itself rather than a named capability
// beneath it.
type SetHealthStateRequest struct {
	Profile    string `json:"profile"`
	Capability string `json:"capability,omitempty"`
	State      string `json:"state"`
	Detail     string `json:"detail,omitempty"`
}

// SetHealthStateResponse is host/set_health_state's result. Applied is false
// when the report was an improvement (healthy) over a state a plugin cannot
// self-clear — recovery is the host's observation to make, not the plugin's
// to assert (§8.1 "plugin can only mark itself worse").
type SetHealthStateResponse struct {
	OK      bool `json:"ok"`
	Applied bool `json:"applied"`
}

// SetHealthState self-reports health for one declared capability.
func (c *Client) SetHealthState(ctx context.Context, req SetHealthStateRequest) (*SetHealthStateResponse, error) {
	var out SetHealthStateResponse
	if err := c.callTool(ctx, toolSetHealthState, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
