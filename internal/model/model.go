package model

import "time"

// RunStatus represents the lifecycle state of an agent run.
// Valid transitions: pending → running → complete | failed
//
//	running → waiting_for_approval → running (approved) | failed (rejected/timeout)
//	running | waiting_for_approval → interrupted (on restart, ADR-011)
type RunStatus string

const (
	RunStatusPending            RunStatus = "pending"
	RunStatusRunning            RunStatus = "running"
	RunStatusWaitingForApproval RunStatus = "waiting_for_approval"
	RunStatusComplete           RunStatus = "complete"
	RunStatusFailed             RunStatus = "failed"
	RunStatusInterrupted        RunStatus = "interrupted"
)

// TriggerType identifies how a policy run is initiated.
type TriggerType string

const (
	TriggerTypeWebhook TriggerType = "webhook"
	TriggerTypeCron    TriggerType = "cron"
	TriggerTypePoll    TriggerType = "poll"
)

// CapabilityRole classifies a tool's access level within a run.
type CapabilityRole string

const (
	CapabilityRoleSensor   CapabilityRole = "sensor"
	CapabilityRoleActuator CapabilityRole = "actuator"
	CapabilityRoleFeedback CapabilityRole = "feedback"
)

// StepType identifies the kind of event recorded in a run's reasoning trace.
type StepType string

const (
	StepTypeThought          StepType = "thought"
	StepTypeToolCall         StepType = "tool_call"
	StepTypeToolResult       StepType = "tool_result"
	StepTypeApprovalRequest  StepType = "approval_request"
	StepTypeFeedbackRequest  StepType = "feedback_request"
	StepTypeFeedbackResponse StepType = "feedback_response"
	StepTypeError            StepType = "error"
	StepTypeComplete         StepType = "complete"
)

// ApprovalMode controls whether a human must approve an actuator call.
type ApprovalMode string

const (
	ApprovalModeNone     ApprovalMode = "none"
	ApprovalModeRequired ApprovalMode = "required"
)

// OnTimeout controls what happens when an approval window expires.
type OnTimeout string

const (
	OnTimeoutReject  OnTimeout = "reject"
	OnTimeoutApprove OnTimeout = "approve"
)

// ConcurrencyPolicy controls behaviour when a trigger fires while a run is active.
type ConcurrencyPolicy string

const (
	ConcurrencySkip     ConcurrencyPolicy = "skip"
	ConcurrencyQueue    ConcurrencyPolicy = "queue"
	ConcurrencyParallel ConcurrencyPolicy = "parallel"
	ConcurrencyReplace  ConcurrencyPolicy = "replace"
)

// ParsedPolicy is the authoritative in-memory representation of a policy's
// configuration, derived from parsing the raw YAML blob stored in the DB.
type ParsedPolicy struct {
	Name         string
	Description  string
	Trigger      TriggerConfig
	Capabilities CapabilitiesConfig
	Agent        AgentConfig
}

// TriggerConfig holds trigger-type-specific fields. Only fields relevant to
// the active TriggerType are populated.
type TriggerConfig struct {
	Type     TriggerType
	Schedule string      // cron only
	Poll     *PollConfig // poll only
}

// PollConfig describes the HTTP poll trigger (v0.3).
type PollConfig struct {
	Interval string
	Request  PollRequest
	Filter   string // JSONPath expression
}

// PollRequest describes the HTTP request made by the poll trigger.
type PollRequest struct {
	URL     string
	Method  string
	Headers map[string]string
	Body    string // POST only
}

// CapabilitiesConfig defines the tool envelope granted to an agent for this run.
// Tools not listed here are never registered with the agent (ADR-001).
type CapabilitiesConfig struct {
	Sensors   []SensorCapability
	Actuators []ActuatorCapability
	Feedback  []string // reserved for future explicit feedback tools
}

// SensorCapability grants a read-only tool to the agent.
type SensorCapability struct {
	Tool string // dot-notation: server_name.tool_name
}

// ActuatorCapability grants a world-affecting tool to the agent, optionally
// with a hard approval gate.
type ActuatorCapability struct {
	Tool      string
	Approval  ApprovalMode
	Timeout   string    // Go duration string; valid only when Approval == required
	OnTimeout OnTimeout // valid only when Approval == required
}

// AgentConfig holds the prompt fields and runtime limits for an agent run.
type AgentConfig struct {
	Preamble    string
	Task        string
	Limits      RunLimits
	Concurrency ConcurrencyPolicy
}

// RunLimits caps resource consumption for an agent run.
type RunLimits struct {
	MaxTokensPerRun    int
	MaxToolCallsPerRun int
}

// GrantedTool is a resolved tool entry used by the agent runner. It pairs the
// tool's MCP identity with its capability classification for this run.
type GrantedTool struct {
	ServerName string
	ToolName   string
	Role       CapabilityRole
	Approval   ApprovalMode  // actuator only
	Timeout    time.Duration // actuator only, zero means no timeout
	OnTimeout  OnTimeout     // actuator only
}
