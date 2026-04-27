import type { ApiMcpTool } from '@/api/types'

// splitToolkit splits a qualified MCP tool name like "Gmail_SendEmail" into
// its toolkit prefix and action. Mirrors internal/arcade.SplitToolkit in Go.
// Arcade's MCP gateway emits names with underscores; the dot form is used
// only by Arcade's REST authorize API (handled server-side).
// Returns { toolkit: '', action: name } when no underscore is present.
export function splitToolkit(qualifiedName: string): { toolkit: string; action: string } {
  const sep = qualifiedName.indexOf('_')
  if (sep === -1) return { toolkit: '', action: qualifiedName }
  return {
    toolkit: qualifiedName.slice(0, sep),
    action: qualifiedName.slice(sep + 1),
  }
}

// groupToolsByToolkit groups tools by their qualified-name prefix.
// The returned Map preserves insertion order (toolkit first-seen order).
// Tools whose name contains no underscore are skipped.
export function groupToolsByToolkit(tools: ApiMcpTool[]): Map<string, ApiMcpTool[]> {
  const result = new Map<string, ApiMcpTool[]>()
  for (const tool of tools) {
    const { toolkit } = splitToolkit(tool.name)
    if (toolkit === '') continue
    const existing = result.get(toolkit)
    if (existing) {
      existing.push(tool)
    } else {
      result.set(toolkit, [tool])
    }
  }
  return result
}
