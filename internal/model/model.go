// Package model defines the domain entity types, enums, and ID generation
// shared across all internal packages.
package model

import (
	"crypto/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// RunStatus represents the lifecycle state of an agent run.
// Valid transitions: pending → running → complete | failed
//
//	running → waiting_for_approval → running (approved) | failed (rejected/timeout)
//	running → waiting_for_feedback → running (response received) | failed
//	running | waiting_for_approval | waiting_for_feedback → interrupted (on restart, ADR-011)
type RunStatus string

const (
	RunStatusPending            RunStatus = "pending"
	RunStatusRunning            RunStatus = "running"
	RunStatusWaitingForApproval RunStatus = "waiting_for_approval"
	RunStatusWaitingForFeedback RunStatus = "waiting_for_feedback"
	RunStatusComplete           RunStatus = "complete"
	RunStatusFailed             RunStatus = "failed"
	RunStatusInterrupted        RunStatus = "interrupted"
)

// TriggerType identifies how a policy run is initiated.
type TriggerType string

const (
	TriggerTypeWebhook   TriggerType = "webhook"
	TriggerTypeManual    TriggerType = "manual"
	TriggerTypeScheduled TriggerType = "scheduled"
	TriggerTypePoll      TriggerType = "poll"
)

// StepType identifies the kind of event recorded in a run's reasoning trace.
type StepType string

const (
	StepTypeCapabilitySnapshot StepType = "capability_snapshot"
	StepTypeThought            StepType = "thought"
	StepTypeThinking           StepType = "thinking"
	StepTypeToolCall           StepType = "tool_call"
	StepTypeToolResult         StepType = "tool_result"
	StepTypeApprovalRequest    StepType = "approval_request"
	StepTypeFeedbackRequest    StepType = "feedback_request"
	StepTypeFeedbackResponse   StepType = "feedback_response"
	StepTypeError              StepType = "error"
	StepTypeComplete           StepType = "complete"
)

// ApprovalMode controls whether a human must approve a tool call.
type ApprovalMode string

const (
	ApprovalModeNone     ApprovalMode = "none"
	ApprovalModeRequired ApprovalMode = "required"
)

// OnTimeout controls what happens when an approval window expires.
type OnTimeout string

const (
	OnTimeoutReject OnTimeout = "reject"
)

// FeedbackOnTimeout controls what happens when a feedback request times out
// without an operator response. This is distinct from OnTimeout (which applies
// to approval gates) because the actions and semantics differ.
type FeedbackOnTimeout string

const (
	FeedbackOnTimeoutFail FeedbackOnTimeout = "fail"
)

func (f FeedbackOnTimeout) String() string { return string(f) }
func (f FeedbackOnTimeout) Valid() bool {
	switch f {
	case FeedbackOnTimeoutFail:
		return true
	}
	return false
}

// MatchMode controls how multiple poll checks are combined.
// all means every check must pass (AND). any means at least one must pass (OR).
type MatchMode string

const (
	MatchAll MatchMode = "all"
	MatchAny MatchMode = "any"
)

func (m MatchMode) String() string { return string(m) }
func (m MatchMode) Valid() bool {
	switch m {
	case MatchAll, MatchAny:
		return true
	}
	return false
}

// Comparator identifies which comparison operation a poll check applies.
type Comparator string

const (
	ComparatorEquals      Comparator = "equals"
	ComparatorNotEquals   Comparator = "not_equals"
	ComparatorGreaterThan Comparator = "greater_than"
	ComparatorLessThan    Comparator = "less_than"
	ComparatorContains    Comparator = "contains"
)

func (c Comparator) String() string { return string(c) }
func (c Comparator) Valid() bool {
	switch c {
	case ComparatorEquals, ComparatorNotEquals, ComparatorGreaterThan, ComparatorLessThan, ComparatorContains:
		return true
	}
	return false
}

// PollCheck is one condition in a poll trigger. On each polling interval,
// the specified MCP tool is called, a JSONPath expression is applied to the
// response, and the resulting value is compared against Value using Comparator.
type PollCheck struct {
	Tool       string         // dot-notation server.tool_name
	Input      map[string]any // static args passed to the MCP tool
	Path       string         // JSONPath expression (e.g. "$.status")
	Comparator Comparator
	Value      any // comparator operand (string, number, or bool)
}

// ConcurrencyPolicy controls behaviour when a trigger fires while a run is active.
type ConcurrencyPolicy string

const (
	ConcurrencySkip     ConcurrencyPolicy = "skip"
	ConcurrencyQueue    ConcurrencyPolicy = "queue"
	ConcurrencyParallel ConcurrencyPolicy = "parallel"
	ConcurrencyReplace  ConcurrencyPolicy = "replace"
)

// DefaultProvider is the LLM provider used when the policy omits the provider field.
const DefaultProvider = "anthropic"

// DefaultModelName is the model ID used when the policy omits the model field.
const DefaultModelName = "claude-sonnet-4-6"

func (s RunStatus) String() string { return string(s) }
func (s RunStatus) Valid() bool {
	switch s {
	case RunStatusPending, RunStatusRunning, RunStatusWaitingForApproval,
		RunStatusWaitingForFeedback, RunStatusComplete, RunStatusFailed, RunStatusInterrupted:
		return true
	}
	return false
}

func (t TriggerType) String() string { return string(t) }
func (t TriggerType) Valid() bool {
	switch t {
	case TriggerTypeWebhook, TriggerTypeManual, TriggerTypeScheduled, TriggerTypePoll:
		return true
	}
	return false
}

func (s StepType) String() string { return string(s) }
func (s StepType) Valid() bool {
	switch s {
	case StepTypeCapabilitySnapshot, StepTypeThought, StepTypeThinking, StepTypeToolCall,
		StepTypeToolResult, StepTypeApprovalRequest, StepTypeFeedbackRequest,
		StepTypeFeedbackResponse, StepTypeError, StepTypeComplete:
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
	case OnTimeoutReject:
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

// Role identifies the access level granted to a user account.
// Users may hold multiple roles simultaneously.
type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
	RoleApprover Role = "approver"
	RoleAuditor  Role = "auditor"
)

func (r Role) String() string { return string(r) }
func (r Role) Valid() bool {
	switch r {
	case RoleAdmin, RoleOperator, RoleApprover, RoleAuditor:
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

// ErrorCode identifies the machine-readable reason an agent run step failed.
// Values are serialized to JSON and stored in SQLite — do not change existing constants.
type ErrorCode string

const (
	ErrorCodeToolError             ErrorCode = "tool_error"
	ErrorCodeAPIError              ErrorCode = "api_error"
	ErrorCodeCancelled             ErrorCode = "cancelled"
	ErrorCodeMissingCapability     ErrorCode = "missing_capability"
	ErrorCodeApprovalRejected      ErrorCode = "approval_rejected"
	ErrorCodeTokenBudgetExceeded   ErrorCode = "token_budget_exceeded"
	ErrorCodeToolCallLimitExceeded ErrorCode = "tool_call_limit_exceeded"
	ErrorCodeSchemaViolation       ErrorCode = "schema_violation"
	ErrorCodeFeedbackTimeout       ErrorCode = "feedback_timeout"
)

func (e ErrorCode) String() string { return string(e) }
func (e ErrorCode) Valid() bool {
	switch e {
	case ErrorCodeToolError, ErrorCodeAPIError, ErrorCodeCancelled, ErrorCodeMissingCapability,
		ErrorCodeApprovalRejected, ErrorCodeTokenBudgetExceeded, ErrorCodeToolCallLimitExceeded,
		ErrorCodeSchemaViolation, ErrorCodeFeedbackTimeout:
		return true
	}
	return false
}

// ErrorStepContent is the structured payload written to audit steps of type error.
// It matches the ErrorContent interface expected by the frontend (frontend/src/components/RunDetail/types.ts).
type ErrorStepContent struct {
	Message string    `json:"message"`
	Code    ErrorCode `json:"code"`
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
	Type          TriggerType
	FireAt        []time.Time   // scheduled only
	WebhookSecret string        `json:"-"` // webhook only; excluded from JSON to prevent secret leakage
	Interval      time.Duration // poll only
	Match         MatchMode     // poll only, defaults to MatchAll
	Checks        []PollCheck   // poll only, at least one required
}

// FeedbackConfig controls the native human-in-the-loop feedback channel.
// When Enabled is true, the runtime injects gleipnir.ask_operator into the
// agent's tool list, allowing it to pause the run and wait for an operator response.
type FeedbackConfig struct {
	Enabled   bool
	Timeout   string            // Go duration string (e.g. "30m"), optional
	OnTimeout FeedbackOnTimeout // default: fail
}

// CapabilitiesConfig defines the tool envelope granted to an agent for this run.
// Tools not listed here are never registered with the agent (ADR-001).
type CapabilitiesConfig struct {
	Tools    []ToolCapability
	Feedback FeedbackConfig
}

// ToolCapability grants a tool to the agent, optionally with a hard approval gate.
type ToolCapability struct {
	Tool      string // dot-notation: server_name.tool_name
	Approval  ApprovalMode
	Timeout   string         // Go duration string; valid only when Approval == required
	OnTimeout OnTimeout      // valid only when Approval == required
	Params    map[string]any // policy-level parameter scoping (ADR-017)
}

// ModelConfig bundles the provider, model name, and provider-specific options
// for an agent run. Options are validated downstream by the provider, not here.
type ModelConfig struct {
	Provider string         `json:"provider"`
	Name     string         `json:"name"`
	Options  map[string]any `json:"options,omitempty"`
}

// DefaultQueueDepth is the maximum number of trigger payloads held in the queue
// when concurrency is "queue" and the policy does not specify queue_depth.
const DefaultQueueDepth = 10

// AgentConfig holds the prompt fields and runtime limits for an agent run.
type AgentConfig struct {
	ModelConfig ModelConfig `json:"model_config"`
	Preamble    string
	Task        string
	Limits      RunLimits
	Concurrency ConcurrencyPolicy
	QueueDepth  int
}

// RunLimits caps resource consumption for an agent run.
type RunLimits struct {
	MaxTokensPerRun    int
	MaxToolCallsPerRun int
}

// GrantedTool is a resolved tool entry used by the agent runner.
type GrantedTool struct {
	ServerName string
	ToolName   string
	Approval   ApprovalMode
	Timeout    time.Duration // zero means no timeout
	OnTimeout  OnTimeout
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
	ID          string
	ServerID    string
	Name        string
	Description string
	InputSchema string // JSON blob
	CreatedAt   time.Time
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

// ApprovalRequest is a pending human-approval gate for a tool call.
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

// User is a registered operator account.
type User struct {
	ID            string
	Username      string
	CreatedAt     time.Time
	DeactivatedAt *time.Time // non-nil when the account has been soft-deleted
}

// Session is an authenticated browser session linked to a user.
type Session struct {
	ID        string
	UserID    string
	Token     string // opaque random value stored in a cookie
	CreatedAt time.Time
	ExpiresAt time.Time
}

// entropyMu guards the monotonic entropy source used by NewULID.
var (
	entropyMu sync.Mutex
	entropy   = ulid.Monotonic(rand.Reader, 0)
)

// NewULID returns a new, lexicographically monotonic ULID string.
// It is safe for concurrent use.
func NewULID() string {
	entropyMu.Lock()
	id := ulid.MustNew(ulid.Now(), entropy)
	entropyMu.Unlock()
	return id.String()
}
