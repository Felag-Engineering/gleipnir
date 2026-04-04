package llm

import "strings"

// ToolNameMapping holds both directions of the MCP-name ↔ wire-name mapping.
// Named fields prevent callers from confusing the two maps, which would
// otherwise both be map[string]string.
type ToolNameMapping struct {
	// SanitizedToOriginal maps wire-format names back to original MCP names.
	// Used by response translators to reverse-map API responses.
	SanitizedToOriginal map[string]string
	// OriginalToSanitized maps original MCP names to wire-format names.
	// Used by message builders to forward-map conversation history.
	OriginalToSanitized map[string]string
}

// SanitizeToolName replaces any character outside [a-zA-Z0-9_] plus any rune
// in allowedExtra with '_', then truncates to 128 characters. The allowedExtra
// parameter lets callers preserve provider-specific characters: Anthropic passes
// "-" (hyphens are valid in Claude tool names) while Google passes "" (hyphens
// must be replaced for the Gemini API).
func SanitizeToolName(name string, allowedExtra string) string {
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		if strings.ContainsRune(allowedExtra, r) {
			return r
		}
		return '_'
	}, name)
	if len(sanitized) > 128 {
		sanitized = sanitized[:128]
	}
	return sanitized
}

// BuildNameMapping creates the bidirectional mapping between original MCP tool
// names and the sanitized wire-format names acceptable to the target provider.
// allowedExtra is forwarded to SanitizeToolName for each tool name.
//
// If two tools sanitize to the same wire name, the later tool silently
// overwrites the earlier one. This matches Google's behavior; Anthropic's
// buildTools handles collisions separately with an explicit error.
func BuildNameMapping(tools []ToolDefinition, allowedExtra string) ToolNameMapping {
	names := ToolNameMapping{
		SanitizedToOriginal: make(map[string]string, len(tools)),
		OriginalToSanitized: make(map[string]string, len(tools)),
	}
	for _, t := range tools {
		sanitized := SanitizeToolName(t.Name, allowedExtra)
		names.SanitizedToOriginal[sanitized] = t.Name
		names.OriginalToSanitized[t.Name] = sanitized
	}
	return names
}
