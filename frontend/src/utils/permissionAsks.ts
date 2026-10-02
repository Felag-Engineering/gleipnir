import type { ApiAttentionItem } from '@/api/types'

// permissionAskRunIds returns the IDs of runs paused on a tool-initiated
// *permission* ask: a `tool_input` attention row whose elicitation_kind is
// `permission` (ADR-055 spec §6.1). Such a run is `waiting_for_feedback` in
// the backend state machine, but to whoever reads its badge it is waiting on
// an approval — see runStatusLabel.
export function permissionAskRunIds(items: readonly ApiAttentionItem[]): ReadonlySet<string> {
  const ids = new Set<string>()
  for (const item of items) {
    if (item.type === 'tool_input' && item.elicitation_kind === 'permission') {
      ids.add(item.run_id)
    }
  }
  return ids
}
