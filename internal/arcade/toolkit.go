package arcade

import (
	"strings"

	"github.com/rapp992/gleipnir/internal/db"
)

// SplitToolkit splits a qualified tool name like "Gmail.SendEmail" into
// its toolkit prefix ("Gmail") and action ("SendEmail"). Only the first dot
// is used as the separator — "Gmail.Send.Email" splits into ("Gmail", "Send.Email").
// Returns ("", name) when no dot is present.
func SplitToolkit(qualifiedName string) (toolkit, action string) {
	toolkit, action, _ = strings.Cut(qualifiedName, ".")
	if action == "" {
		// No dot found: strings.Cut returns (qualifiedName, "", false).
		return "", qualifiedName
	}
	return toolkit, action
}

// GroupToolsByToolkit groups tools by their qualified-name prefix.
// Map keys are toolkit names; values preserve input order.
// Tools whose name contains no dot (i.e. SplitToolkit returns an empty toolkit)
// are skipped — they do not belong to any Arcade toolkit.
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
