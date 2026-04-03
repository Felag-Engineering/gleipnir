import { useState } from 'react'
import {
  parseStep,
  pairToolBlocks,
  isToolBlock,
} from '@/components/RunDetail'
import type { FilterKey } from '@/components/RunDetail'
import type { ParsedStep, ToolBlockData } from '@/components/RunDetail/types'
import type { ApiRunStep } from '@/api/types'

const PAGE_SIZE = 50

export interface RunTimelineResult {
  timelineItems: (ParsedStep | ToolBlockData)[]
  filteredItems: (ParsedStep | ToolBlockData)[]
  snapshotSteps: ParsedStep[]
  counts: Record<FilterKey, number>
  hasMore: boolean
  remainingCount: number
  loadMore: () => void
}

// useRunTimeline owns all step data processing: parsing, snapshot separation,
// tool block pairing, filter counts, filtering, and client-side pagination.
export function useRunTimeline(
  rawSteps: ApiRunStep[],
  filter: FilterKey,
): RunTimelineResult {
  const [displayedCount, setDisplayedCount] = useState(PAGE_SIZE)

  const allParsed: ParsedStep[] = rawSteps.map(parseStep)

  // Separate capability_snapshot steps; filter counts exclude them (ADR-018)
  const nonSnapshotSteps = allParsed.filter((s) => s.type !== 'capability_snapshot')
  const snapshotSteps = allParsed.filter((s) => s.type === 'capability_snapshot')

  // Pair tool_call/tool_result/approval_request steps into visual ToolBlock units.
  // Filtering and pagination operate on these paired items so that a tool interaction
  // counts as one item regardless of how many raw steps it spans.
  const pairedItems = pairToolBlocks(nonSnapshotSteps)

  // Count each filter category over paired visual items (not raw steps).
  // "All" reflects what the user sees on screen — one entry per visual block.
  const counts: Record<FilterKey, number> = {
    all: pairedItems.length,
    tool: pairedItems.filter(isToolBlock).length,
    thought: pairedItems.filter((item) => !isToolBlock(item) && item.type === 'thought').length,
    thinking: pairedItems.filter((item) => !isToolBlock(item) && item.type === 'thinking').length,
    error: pairedItems.filter((item) =>
      isToolBlock(item)
        ? item.result?.content.is_error === true
        : item.type === 'error'
    ).length,
    approval: pairedItems.filter((item) =>
      isToolBlock(item)
        ? item.approval !== null
        : item.type === 'approval_request'
    ).length,
  }

  // Apply filter to paired items. ToolBlockData always matches 'tool'. Filtering by
  // 'error' includes both standalone error steps and tool blocks with an error result.
  // Filtering by 'approval' includes blocks with any approval state plus standalone
  // approval_request steps.
  const filteredItems =
    filter === 'all'
      ? pairedItems
      : pairedItems.filter((item) => {
          if (isToolBlock(item)) {
            switch (filter) {
              case 'tool': return true
              case 'error': return item.result?.content.is_error === true
              case 'approval': return item.approval !== null
              default: return false
            }
          }
          switch (filter) {
            case 'thought': return item.type === 'thought'
            case 'thinking': return item.type === 'thinking'
            case 'error': return item.type === 'error'
            case 'approval': return item.type === 'approval_request'
            // Types without a dedicated filter category (feedback_request, feedback_response,
            // complete, orphan tool_result) are only visible under the 'all' filter. This is
            // intentional — these are rare step types that don't warrant their own chip.
            default: return false
          }
        })

  // Client-side pagination
  const displayedItems = filteredItems.slice(0, displayedCount)
  const hasMore = filteredItems.length > displayedCount

  // Capability snapshots are now rendered in the header, not in the timeline.
  const timelineItems: (ParsedStep | ToolBlockData)[] = displayedItems

  const remainingCount = filteredItems.length - displayedItems.length

  function loadMore() {
    setDisplayedCount((c) => c + PAGE_SIZE)
  }

  return { timelineItems, filteredItems, snapshotSteps, counts, hasMore, remainingCount, loadMore }
}
