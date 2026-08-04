package model

// ElicitationKind splits a tool-initiated human-in-the-loop request into the
// two things an operator can actually be asked for (ADR-055,
// mcp-realignment-spec.md §6.1).
//
// The distinction is not cosmetic — it decides who may answer. A consent-only
// ask is an authorization decision and carries the same weight as an ADR-008
// approval, so it needs an approver. Supplying a missing value is ordinary
// operating work, so it needs an operator. Both roles exist precisely because
// the org may not want them to be the same person.
//
// The type lives in model, not in the agent runtime that classifies, because
// the classification is written by the runtime and read by the API layer's
// role gate. A shared vocabulary keeps the two from drifting into disagreement
// about what a persisted "permission" row means.
type ElicitationKind string

const (
	// ElicitationKindPermission is a consent-only ask: the server wants a yes
	// or a no and requests no fields. Rendered as approve/reject.
	ElicitationKindPermission ElicitationKind = "permission"

	// ElicitationKindInformation is a request for values the server needs to
	// continue. Rendered as a form.
	ElicitationKindInformation ElicitationKind = "information"
)

func (k ElicitationKind) String() string { return string(k) }

// Valid reports whether k is one of the two known kinds. The
// tool_input_requests.elicitation_kind column has a matching CHECK constraint,
// so an invalid value cannot reach the database — this guards the other
// direction, where a value arrives from a server's _meta hint.
func (k ElicitationKind) Valid() bool {
	switch k {
	case ElicitationKindPermission, ElicitationKindInformation:
		return true
	}
	return false
}

// RequiredRole returns the role that may resolve a request of this kind
// (spec §6.1: permission ⇒ approver, information ⇒ operator). Admins bypass
// every role guard in this codebase, so they are not named here.
//
// An unknown kind returns RoleAdmin — a value the classifier cannot produce
// and the DB CHECK constraint cannot store. If one somehow appears, the safe
// reading is "nobody but an admin", not "anybody".
func (k ElicitationKind) RequiredRole() Role {
	switch k {
	case ElicitationKindPermission:
		return RoleApprover
	case ElicitationKindInformation:
		return RoleOperator
	}
	return RoleAdmin
}
