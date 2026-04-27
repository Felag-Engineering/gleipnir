package arcade

import (
	"strings"

	"github.com/felag-engineering/gleipnir/internal/db"
)

// SplitToolkit splits a qualified MCP tool name like "Gmail_SendEmail" into
// its toolkit prefix ("Gmail") and action ("SendEmail"). Only the first
// underscore is used as the separator — "Gmail_Send_Email" splits into
// ("Gmail", "Send_Email"). Returns ("", name) when no underscore is present.
//
// Note: Arcade emits tool names with underscores via the MCP `tools/list`
// transport, but its REST /v1/tools/authorize endpoint expects the same names
// with dot separators (e.g. "Gmail.SendEmail"). Use AuthorizeToolName to
// convert before calling that REST endpoint.
func SplitToolkit(qualifiedName string) (toolkit, action string) {
	toolkit, action, _ = strings.Cut(qualifiedName, "_")
	if action == "" {
		// No underscore found: strings.Cut returns (qualifiedName, "", false).
		return "", qualifiedName
	}
	return toolkit, action
}

// AuthorizeToolName converts an MCP-form tool name (underscore-separated) to
// the dot-separated form Arcade's REST /v1/tools/authorize endpoint expects.
// "Gmail_SendEmail" → "Gmail.SendEmail". Names without an underscore are
// returned unchanged.
func AuthorizeToolName(mcpName string) string {
	return strings.Replace(mcpName, "_", ".", 1)
}

// GroupToolsByToolkit groups tools by their qualified-name prefix.
// Map keys are toolkit names; values preserve input order.
// Tools whose name contains no underscore (i.e. SplitToolkit returns an empty
// toolkit) are skipped — they do not belong to any Arcade toolkit.
func GroupToolsByToolkit(tools []db.McpTool) map[string][]db.McpTool {
	result := make(map[string][]db.McpTool)
	for _, tool := range tools {
		tk, _ := SplitToolkit(tool.Name)
		if tk == "" {
			continue
		}
		result[tk] = append(result[tk], tool)
	}
	return result
}
