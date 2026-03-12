// Package model defines the domain entity types, enums, and ID generation
// shared across all internal packages.
package model

import (
	"math/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

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
	TriggerTypeWebhook   TriggerType = "webhook"
	TriggerTypeCron      TriggerType = "cron"
	TriggerTypePoll      TriggerType = "poll"
	TriggerTypeManual    TriggerType = "manual"
	TriggerTypeScheduled TriggerType = "scheduled"
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
	StepTypeCapabilitySnapshot StepType = "capability_snapshot"
	StepTypeThought            StepType = "thought"
	StepTypeToolCall           StepType = "tool_call"
	StepTypeToolResult         StepType = "tool_result"
	StepTypeApprovalRequest    StepType = "approval_request"
	StepTypeFeedbackRequest    StepType = "feedback_request"
	StepTypeFeedbackResponse   StepType = "feedback_response"
	StepTypeError              StepType = "error"
	StepTypeComplete           StepType = "complete"
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

func (s RunStatus) String() string { return string(s) }
func (s RunStatus) Valid() bool {
	switch s {
	case RunStatusPending, RunStatusRunning, RunStatusWaitingForApproval,
		RunStatusComplete, RunStatusFailed, RunStatusInterrupted:
		return true
	}
	return false
}

func (t TriggerType) String() string { return string(t) }
func (t TriggerType) Valid() bool {
	switch t {
	case TriggerTypeWebhook, TriggerTypeCron, TriggerTypePoll, TriggerTypeManual, TriggerTypeScheduled:
		return true
	}
	return false
}

func (r CapabilityRole) String() string { return string(r) }
func (r CapabilityRole) Valid() bool {
	switch r {
	case CapabilityRoleSensor, CapabilityRoleActuator, CapabilityRoleFeedback:
		return true
	}
	return false
}

func (s StepType) String() string { return string(s) }
func (s StepType) Valid() bool {
	switch s {
	case StepTypeCapabilitySnapshot, StepTypeThought, StepTypeToolCall, StepTypeToolResult,
		StepTypeApprovalRequest, StepTypeFeedbackRequest, StepTypeFeedbackResponse,
		StepTypeError, StepTypeComplete:
		return true
	}
	return false
}

func (m ApprovalMode) String() string { return string(m) }
func (m ApprovalMode) Valid() bool {
	switch m {
	case ApprovalModeNone, ApprovalModeRequired:
		return true
	}
	return false
}

func (t OnTimeout) String() string { return string(t) }
func (t OnTimeout) Valid() bool {
	switch t {
	case OnTimeoutReject, OnTimeoutApprove:
		return true
	}
	return false
}

func (c ConcurrencyPolicy) String() string { return string(c) }
func (c ConcurrencyPolicy) Valid() bool {
	switch c {
	case ConcurrencySkip, ConcurrencyQueue, ConcurrencyParallel, ConcurrencyReplace:
		return true
	}
	return false
}

// ApprovalStatus tracks the lifecycle of a human approval request.
type ApprovalStatus string

const (
	ApprovalStatusPending  ApprovalStatus = "pending"
	ApprovalStatusApproved ApprovalStatus = "approved"
	ApprovalStatusRejected ApprovalStatus = "rejected"
	ApprovalStatusTimeout  ApprovalStatus = "timeout"
)

func (s ApprovalStatus) String() string { return string(s) }
func (s ApprovalStatus) Valid() bool {
	switch s {
	case ApprovalStatusPending, ApprovalStatusApproved, ApprovalStatusRejected, ApprovalStatusTimeout:
		return true
	}
	return false
}

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
	FireAt   []time.Time // scheduled only
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
	Tool   string         // dot-notation: server_name.tool_name
	Params map[string]any // policy-level parameter scoping (ADR-017)
}

// ActuatorCapability grants a world-affecting tool to the agent, optionally
// with a hard approval gate.
type ActuatorCapability struct {
	Tool      string
	Approval  ApprovalMode
	Timeout   string         // Go duration string; valid only when Approval == required
	OnTimeout OnTimeout      // valid only when Approval == required
	Params    map[string]any // policy-level parameter scoping (ADR-017)
}

// AgentConfig holds the prompt fields and runtime limits for an agent run.
type AgentConfig struct {
	Model       string
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
	Approval   ApprovalMode   // actuator only
	Timeout    time.Duration  // actuator only, zero means no timeout
	OnTimeout  OnTimeout      // actuator only
	Params     map[string]any // policy-level parameter scoping (ADR-017)
}

// MCPServer represents a registered MCP tool server.
type MCPServer struct {
	ID               string
	Name             string
	URL              string
	LastDiscoveredAt *time.Time
	CreatedAt        time.Time
}

// MCPTool is a tool discovered from an MCP server.
type MCPTool struct {
	ID             string
	ServerID       string
	Name           string
	Description    string
	InputSchema    string // JSON blob
	CapabilityRole CapabilityRole
	CreatedAt      time.Time
}

// Policy is the domain entity for a stored policy record.
type Policy struct {
	ID          string
	Name        string
	TriggerType TriggerType
	YAML        string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	PausedAt    *time.Time // non-nil for paused scheduled policies
}

// Run is a single agent execution scoped to a policy.
type Run struct {
	ID             string
	PolicyID       string
	Status         RunStatus
	TriggerType    TriggerType
	TriggerPayload string // JSON blob
	StartedAt      time.Time
	CompletedAt    *time.Time
	TokenCost      int64
	Error          *string
	ThreadID       *string
	CreatedAt      time.Time
}

// RunStep is one entry in a run's reasoning trace.
type RunStep struct {
	ID         string
	RunID      string
	StepNumber int64
	Type       StepType
	Content    string // JSON blob
	TokenCost  int64
	CreatedAt  time.Time
}

// ApprovalRequest is a pending human-approval gate for an actuator call.
type ApprovalRequest struct {
	ID               string
	RunID            string
	ToolName         string
	ProposedInput    string // JSON blob
	ReasoningSummary string
	Status           ApprovalStatus
	DecidedAt        *time.Time
	ExpiresAt        time.Time
	Note             *string
	CreatedAt        time.Time
}

// entropyMu guards the monotonic entropy source used by NewULID.
var (
	entropyMu sync.Mutex
	entropy   = ulid.Monotonic(rand.New(rand.NewSource(time.Now().UnixNano())), 0) //nolint:gosec
)

// NewULID returns a new, lexicographically monotonic ULID string.
// It is safe for concurrent use.
func NewULID() string {
	entropyMu.Lock()
	id := ulid.MustNew(ulid.Now(), entropy)
	entropyMu.Unlock()
	return id.String()
}
