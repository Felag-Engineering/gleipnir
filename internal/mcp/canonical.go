package mcp

import (
	"bytes"
	"encoding/json"
	"log/slog"

	"github.com/felag-engineering/gleipnir/internal/schemanorm"
)

// DiscoveredTool pairs a wire-decoded Tool with its schemanorm-normalized
// input schema. Produced only by the canonicalizing discovery paths
// (ProbeTools, RefreshTools); CanonicalSchema == nil means "no canonical
// form" -- either the tool's inputSchema was absent, or normalization failed
// and was logged.
//
// This is a wrapper rather than a field on Tool: Tool is the wire-decoded
// shape (client.go), converted directly from toolWire via a struct
// conversion (`Tool(tw)`) that would break if the field sets diverged, and
// fakeserver.go also uses Tool to describe a server-side tool list, where a
// canonical column has no meaning.
type DiscoveredTool struct {
	Tool
	CanonicalSchema json.RawMessage
}

// CanonicalSchemaPtr returns nil when CanonicalSchema is empty, else a
// pointer to its string form. Exported so internal/http/api's server-create
// path can apply the same nil-means-no-canonical-form mapping when building
// db.UpsertMCPToolParams -- this is the single definition of that mapping.
func (d DiscoveredTool) CanonicalSchemaPtr() *string {
	if len(d.CanonicalSchema) == 0 {
		return nil
	}
	s := string(d.CanonicalSchema)
	return &s
}

// canonicalizeDiscovered runs schemanorm.Normalize over each tool's
// InputSchema, pairing the result alongside the original Tool. Normalization
// never drops a tool: on failure, the tool is still returned with
// CanonicalSchema nil and a WARN is logged (fail-open at discovery).
func canonicalizeDiscovered(serverID, serverName string, tools []Tool) []DiscoveredTool {
	discovered := make([]DiscoveredTool, len(tools))
	for i, t := range tools {
		discovered[i] = DiscoveredTool{Tool: t}

		// An absent inputSchema is legal (see anthropic/client.go's len==0
		// handling) and is not a failure -- logging it would produce a WARN
		// per schema-less tool on every single refresh.
		if len(bytes.TrimSpace(t.InputSchema)) == 0 {
			continue
		}

		canonical, err := schemanorm.Normalize(t.InputSchema)
		if err != nil {
			// Pass err itself, never %s-format it into the message. The
			// primary guarantee is structural: this is a slog attribute, and
			// the server installs a JSON handler that escapes newlines/ESC/
			// quotes in every attribute value regardless of what produced
			// it -- including marshalDeterministic's plain wrapped error,
			// which is not a *schemanorm.Error. As defense in depth,
			// *schemanorm.Error.Error() additionally renders its own JSON
			// Pointer with %q. Never log the schema bytes.
			slog.Warn("mcp tool schema failed normalization; storing raw schema with NULL canonical",
				"server_id", serverID, "server_name", serverName, "tool_name", t.Name, "err", err)
			continue
		}
		discovered[i].CanonicalSchema = canonical
	}
	return discovered
}

// toolSchemaChanged is the drift predicate RefreshTools uses to decide
// whether a tool changed since the last discovery: it prefers comparing the
// canonical form (so a key-order-only schema change does not spuriously flag
// drift) and falls back to raw byte comparison when either side has no
// canonical form.
//
// Two consequences follow from the fallback:
//
//  1. On the FIRST refresh after upgrading to this column, every stored row
//     has NULL canonical, so that refresh compares raw -- identical to
//     pre-upgrade behavior, including flagging a key-order-only change as
//     Modified once. The same refresh's upsert backfills canonical for every
//     tool it touches, so every later refresh uses the canonical path.
//  2. An empty-string stored canonical (oldCanonical != nil but *oldCanonical
//     == "") is treated identically to NULL.
func toolSchemaChanged(oldRaw string, oldCanonical *string, freshRaw, freshCanonical json.RawMessage) bool {
	oc := ""
	if oldCanonical != nil {
		oc = *oldCanonical
	}
	fc := string(freshCanonical)

	if oc != "" && fc != "" {
		return oc != fc
	}
	return oldRaw != string(freshRaw)
}
