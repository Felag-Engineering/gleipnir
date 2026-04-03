import { useState, useRef, useEffect, useCallback } from 'react'
import { useParams } from 'react-router-dom'
import { useRun } from '@/hooks/queries/runs'
import { useRunSteps } from '@/hooks/queries/runs'
import SkeletonBlock from '@/components/SkeletonBlock/SkeletonBlock'
import { QueryBoundary } from '@/components/QueryBoundary'
import { EmptyState } from '@/components/EmptyState'
import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import {
  RunHeader,
  FilterBar,
  StepTimeline,
  parseStep,
  pairToolBlocks,
  isToolBlock,
} from '@/components/RunDetail'
import type { FilterKey } from '@/components/RunDetail'
import type { ParsedStep, ToolBlockData } from '@/components/RunDetail/types'
import { usePageTitle } from '@/hooks/usePageTitle'
import styles from './RunDetailPage.module.css'

const PAGE_SIZE = 50

export default function RunDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { data: run, status: runStatus, refetch: runRefetch } = useRun(id)
  const { data: rawSteps = [], status: stepsStatus } = useRunSteps(id)

  usePageTitle(id ? `Run ${id.slice(0, 8)}` : 'Run')
  const [filter, setFilter] = useState<FilterKey>('all')
  const [displayedCount, setDisplayedCount] = useState(PAGE_SIZE)
  const [showNewPill, setShowNewPill] = useState(false)
  const [isNearBottom, setIsNearBottom] = useState(true)

  const sentinelRef = useRef<HTMLDivElement>(null)
  const prevStepCount = useRef(0)

  // Parse all steps once
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

  // Show capability snapshots at the top always; then the paginated filtered items.
  const timelineItems: (ParsedStep | ToolBlockData)[] = [
    ...snapshotSteps,
    ...displayedItems,
  ]

  // IntersectionObserver for scroll detection (not available in all test environments)
  useEffect(() => {
    const sentinel = sentinelRef.current
    if (!sentinel || typeof IntersectionObserver === 'undefined') return
    const observer = new IntersectionObserver(
      ([entry]) => {
        setIsNearBottom(entry.isIntersecting)
      },
      { threshold: 0.1 },
    )
    observer.observe(sentinel)
    return () => observer.disconnect()
  }, [])

  // Show "New steps ↓" pill when steps grow and user isn't near bottom
  useEffect(() => {
    const newCount = rawSteps.length
    if (newCount > prevStepCount.current && !isNearBottom) {
      setShowNewPill(true)
    }
    prevStepCount.current = newCount
  }, [rawSteps.length, isNearBottom])

  const scrollToBottom = useCallback(() => {
    sentinelRef.current?.scrollIntoView({ behavior: 'smooth' })
    setShowNewPill(false)
  }, [])

  // Duration computation
  const duration =
    run
      ? run.completed_at
        ? new Date(run.completed_at).getTime() - new Date(run.started_at).getTime()
        : Date.now() - new Date(run.started_at).getTime()
      : null
  const durationSeconds = duration !== null ? duration / 1000 : null

  const toolCallCount = allParsed.filter((s) => s.type === 'tool_call').length
  const tokenTotal = rawSteps.reduce((acc, s) => acc + s.token_cost, 0)

  const isLoading = runStatus === 'pending' || stepsStatus === 'pending'
  const runError = runStatus === 'error'
  const boundaryStatus = isLoading ? 'pending' : runError ? 'error' : 'success'

  return (
    <div className={styles.page}>
      <QueryBoundary
        status={boundaryStatus}
        isEmpty={!run}
        errorMessage="Failed to load run. It may not exist or the server may be unavailable."
        onRetry={() => { void runRefetch() }}
        emptyState={
          <EmptyState
            headline="Run not found"
            subtext="The run you're looking for doesn't exist or may have been deleted."
            ctaLabel="Back to runs"
            ctaTo="/runs"
          />
        }
        skeleton={
          <div className={styles.skeleton}>
            <SkeletonBlock height={48} />
            <SkeletonBlock height={24} width="60%" />
            <SkeletonBlock height={80} />
            <SkeletonBlock height={40} />
            <SkeletonBlock height={120} />
            <SkeletonBlock height={120} />
          </div>
        }
      >
        {run && (
          <ErrorBoundary>
            <RunHeader
              run={run}
              toolCallCount={toolCallCount}
              tokenTotal={tokenTotal}
              duration={duration}
            />

            {(run.status === 'failed' || run.status === 'interrupted') && run.error && (
              <div className={styles.errorBox} role="alert">
                <span className={styles.errorBoxLabel}>
                  {run.status === 'failed' ? 'Run failed' : 'Run interrupted'}
                </span>
                <pre className={styles.errorBoxMsg}>{run.error}</pre>
              </div>
            )}

            {run.trigger_payload && run.trigger_payload !== '{}' && run.trigger_payload !== 'null' && (
              <div className={styles.triggerPayload}>
                <h2 className={styles.sectionTitle}>Trigger payload</h2>
                <CollapsibleJSON
                  value={(() => {
                    try { return JSON.parse(run.trigger_payload!) } catch { return run.trigger_payload }
                  })()}
                />
              </div>
            )}

            <FilterBar active={filter} counts={counts} onChange={setFilter} />

            <div className={styles.timeline}>
              <StepTimeline items={timelineItems} systemPrompt={run.system_prompt} runId={id!} runStatus={run.status} durationSeconds={durationSeconds} />

              {hasMore && (
                <button
                  type="button"
                  className={styles.loadMoreBtn}
                  onClick={() => setDisplayedCount((c) => c + PAGE_SIZE)}
                >
                  Load more ({filteredItems.length - displayedCount} remaining)
                </button>
              )}
            </div>

            <div ref={sentinelRef} className={styles.sentinel} aria-hidden="true" />

            {showNewPill && (
              <button
                type="button"
                className={styles.newStepsPill}
                onClick={scrollToBottom}
              >
                New steps ↓
              </button>
            )}
          </ErrorBoundary>
        )}
      </QueryBoundary>
    </div>
  )
}
