// parseToolOutput extracts a display-ready value from the raw JSON string stored
// in ToolResultContent.output. The MCP protocol wraps text responses in a
// content-array envelope; per ADR-030 the UI abstracts over transport details,
// so a text-only envelope is collapsed to a plain string.
export function parseToolOutput(raw: string): unknown {
  let value: unknown
  try {
    value = JSON.parse(raw)
  } catch {
    return raw
  }

  // MCP content envelope: an array whose items are { type, text, ... }.
  // Collapse to a single newline-joined string only when every item is
  // { type: "text", text: string }. Mixed arrays (e.g. text + image) fall
  // through to the JSON view so no content is silently dropped.
  if (Array.isArray(value) && value.length > 0) {
    const allText = value.every(
      (item) =>
        typeof item === 'object' &&
        item !== null &&
        (item as Record<string, unknown>).type === 'text' &&
        typeof (item as Record<string, unknown>).text === 'string',
    )
    if (allText) {
      return value.map((item) => (item as { text: string }).text).join('\n')
    }
  }

  return value
}
