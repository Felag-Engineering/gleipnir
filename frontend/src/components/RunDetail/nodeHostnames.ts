// Maps Relay node ids to the hostnames a run already learned from list_nodes,
// so the fan-out table can label a row "dev-node-4" instead of only
// "node-814e5b14…". Relay's per-Node results carry node_id and never a
// hostname (PerNodeResult, relay/internal/controlplane/mcp/wire.go), but every
// list_nodes result carries both (NodeItem.node_id + facts.hostname).
//
// Display-only, and deliberately fail-soft: anything unexpected yields no
// entry, and a row without an entry shows its node_id exactly as before.
import { parseToolOutput } from './toolOutput'
import { isToolBlock } from './types'
import type { ParsedStep, ToolBlockData } from './types'

// NodeHostnameIndex is keyed by MCP server (the tool_call's server_id), then
// by node_id. Scoping by server means a list_nodes result from one server can
// never relabel the rows of another server's fan-out result.
export type NodeHostnameIndex = ReadonlyMap<string, ReadonlyMap<string, string>>

export const EMPTY_HOSTNAME_INDEX: NodeHostnameIndex = new Map()

// The longest label shown. A DNS name can be 253 characters, but a table cell
// is not the place for one; the full node_id stays beside it either way.
export const MAX_HOSTNAME_LENGTH = 63

// facts.hostname is reported by the Node itself, so a compromised Node chooses
// it. Restricting it to hostname characters keeps a Node from naming itself
// something that reads like part of the table ("dev-node-4 ✓ success"), or
// from smuggling bidi/control characters into the cell.
const HOSTNAME_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]*$/

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function displayHostname(raw: unknown): string | null {
  if (typeof raw !== 'string') return null
  const hostname = raw.trim()
  if (hostname === '' || !HOSTNAME_PATTERN.test(hostname)) return null
  return hostname.length > MAX_HOSTNAME_LENGTH ? `${hostname.slice(0, MAX_HOSTNAME_LENGTH - 1)}…` : hostname
}

// parseListNodesHostnames reads one list_nodes result (the value
// parseToolOutput returns: the JSON text, or an already-parsed object) into
// node_id → hostname pairs. Unlike the fan-out parser it ignores fields it
// does not know: this is a lookup, and nothing the operator would otherwise
// see is hidden by skipping a field. Returns an empty array for anything that
// is not list_nodes-shaped.
export function parseListNodesHostnames(output: unknown): [string, string][] {
  let value: unknown = output
  if (typeof output === 'string') {
    const trimmed = output.trim()
    if (!trimmed.startsWith('{')) return []
    try {
      value = JSON.parse(trimmed)
    } catch {
      return []
    }
  }
  if (!isPlainObject(value) || !Array.isArray(value.nodes)) return []

  const pairs: [string, string][] = []
  for (const node of value.nodes) {
    if (!isPlainObject(node) || typeof node.node_id !== 'string' || node.node_id === '') continue
    if (!isPlainObject(node.facts)) continue
    const hostname = displayHostname(node.facts.hostname)
    if (hostname !== null) pairs.push([node.node_id, hostname])
  }
  return pairs
}

// isListNodesBlock matches a successful list_nodes call. MCP tool names are
// registered as "<server name>.<tool>", and the tool_call step's server_id is
// that same server name, so requiring both to agree pins the call to the
// server whose rows the result will label.
function isListNodesBlock(block: ToolBlockData): block is ToolBlockData & {
  call: NonNullable<ToolBlockData['call']>
  result: NonNullable<ToolBlockData['result']>
} {
  if (!block.call || !block.result || block.result.content.is_error) return false
  const { server_id: serverId, tool_name: toolName } = block.call.content
  return serverId !== '' && toolName === `${serverId}.list_nodes`
}

// buildNodeHostnameIndex collects every successful list_nodes result in a run,
// per server. A later result overrides an earlier one for the same node_id.
//
// A hostname claimed by more than one node_id is dropped for all of them:
// Nodes report their own hostnames, so two rows reading "dev-node-4" would
// let one Node pass as another at a glance. Those rows keep their node_ids.
export function buildNodeHostnameIndex(items: readonly (ParsedStep | ToolBlockData)[]): NodeHostnameIndex {
  const byServer = new Map<string, Map<string, string>>()

  for (const item of items) {
    if (!isToolBlock(item) || !isListNodesBlock(item)) continue
    const pairs = parseListNodesHostnames(parseToolOutput(item.result.content.output))
    if (pairs.length === 0) continue

    const serverId = item.call.content.server_id
    let hostnames = byServer.get(serverId)
    if (!hostnames) {
      hostnames = new Map()
      byServer.set(serverId, hostnames)
    }
    for (const [nodeId, hostname] of pairs) hostnames.set(nodeId, hostname)
  }

  for (const hostnames of byServer.values()) {
    const claims = new Map<string, number>()
    for (const hostname of hostnames.values()) claims.set(hostname, (claims.get(hostname) ?? 0) + 1)
    for (const [nodeId, hostname] of hostnames) {
      if (claims.get(hostname)! > 1) hostnames.delete(nodeId)
    }
  }

  return byServer
}
