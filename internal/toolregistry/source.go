// Package toolregistry provides a cross-source in-memory uniqueness arbiter
// for tool dot-names (e.g. "my-server.read_pods"). Both the MCP side
// (internal/mcp) and the plugin side (internal/plugin/*) import this package;
// neither imports the other, keeping the package boundary clean.
//
// The uniqueness invariant: every dot-name "source.tool" may be reserved by
// at most one source at a time. Conflict on registration → caller must reject
// the second registrant.
package toolregistry

import "errors"

// Kind identifies the type of source that owns a tool namespace reservation.
type Kind int

const (
	KindMCP    Kind = iota // an MCP server registration
	KindPlugin             // a plugin instance registration
)

// Source is the owner of a tool namespace reservation: either an MCP server
// or a plugin instance, identified by name.
type Source struct {
	Kind Kind
	Name string
}

// String returns the human-readable representation used in API responses and
// audit events: "mcp:<name>" or "plugin:<name>".
func (s Source) String() string {
	switch s.Kind {
	case KindMCP:
		return "mcp:" + s.Name
	case KindPlugin:
		return "plugin:" + s.Name
	default:
		return "unknown:" + s.Name
	}
}

// DotName joins a source name and a tool name with a dot, producing the
// canonical "source.tool" dot-notation. Callers are responsible for ensuring
// neither part contains a dot — the policy and plugin loaders validate names
// upstream.
func DotName(source, tool string) string {
	return source + "." + tool
}

// ErrConflict is the sentinel for tool namespace conflicts. Use errors.Is to
// check; use errors.As to recover the ConflictError with conflict identity.
var ErrConflict = errors.New("tool namespace already registered")

// ConflictError carries the dot-name and the existing owner when a reservation
// attempt fails because the slot is already held by a different source.
type ConflictError struct {
	// DotName is the conflicting "source.tool" name.
	DotName string
	// Existing is the source that currently owns the name.
	Existing Source
}

func (e *ConflictError) Error() string {
	return "tool namespace conflict: " + e.DotName + " is already registered to " + e.Existing.String()
}

// Unwrap returns ErrConflict so callers can use errors.Is(err, ErrConflict).
func (e *ConflictError) Unwrap() error {
	return ErrConflict
}

// Reservation is a single (dot-name, owner) pair used as input to ReserveBulk.
type Reservation struct {
	DotName string
	Owner   Source
}
