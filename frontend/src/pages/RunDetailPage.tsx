import { useState, useRef, useEffect, useCallback } from 'react'
import { useParams } from 'react-router-dom'
import { useRun } from '@/hooks/useRun'
import { useRunSteps } from '@/hooks/useRunSteps'
import SkeletonBlock from '@/components/SkeletonBlock/SkeletonBlock'
import { QueryBoundary } from '@/components/QueryBoundary'
import { EmptyState } from '@/components/EmptyState'
import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import {
  RunHeader,
  MetadataGrid,
  FilterBar,
  StepTimeline,
  parseStep,
} from '@/components/RunDetail'
import type { FilterKey } from '@/components/RunDetail'
import type { ParsedStep, CapabilitySnapshotContent, CapabilitySnapshotV2, GrantedToolEntry } from '@/components/RunDetail/types'
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

  // Build tool role map from capability_snapshot steps. Handles both the
  // legacy array shape (pre-ADR-023) and the V2 object shape { model, tools }.
  const toolRoleMap = new Map<string, GrantedToolEntry['Role']>()
  for (const step of allParsed) {
    if (step.type === 'capability_snapshot') {
      const raw = step.content as CapabilitySnapshotContent
      const tools: GrantedToolEntry[] = Array.isArray(raw)
        ? raw
        : (raw as CapabilitySnapshotV2).tools ?? []
      for (const entry of tools) {
        toolRoleMap.set(`${entry.ServerName}.${entry.ToolName}`, entry.Role)
      }
    }
  }

  // Separate capability_snapshot steps; filter counts exclude them (ADR-018)
  const nonSnapshotSteps = allParsed.filter((s) => s.type !== 'capability_snapshot')
  const snapshotSteps = allParsed.filter((s) => s.type === 'capability_snapshot')

  // Filter counts. Each tool_call represents one tool interaction (paired with
  // its result in the timeline), so we count calls only — not results separately.
  const counts: Record<FilterKey, number> = {
    all: nonSnapshotSteps.length,
    thought: nonSnapshotSteps.filter((s) => s.type === 'thought').length,
    tool: nonSnapshotSteps.filter((s) => s.type === 'tool_call').length,
    error: nonSnapshotSteps.filter((s) => s.type === 'error').length,
  }

  // Apply filter. When filtering by 'tool', include approval_request and tool_result
  // steps alongside tool_call so that pairToolBlocks can still form complete blocks.
  const filteredSteps =
    filter === 'all'
      ? nonSnapshotSteps
      : filter === 'tool'
        ? nonSnapshotSteps.filter(
            (s) =>
              s.type === 'tool_call' ||
              s.type === 'tool_result' ||
              s.type === 'approval_request',
          )
        : nonSnapshotSteps.filter((s) => s.type === filter)

  // Client-side pagination
  const displayedSteps = filteredSteps.slice(0, displayedCount)
  const hasMore = filteredSteps.length > displayedCount

  // Show capability snapshot at the top always
  const timelineSteps: ParsedStep[] = [
    ...snapshotSteps,
    ...displayedSteps,
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
            <RunHeader run={run} />

            <MetadataGrid
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
              <StepTimeline steps={timelineSteps} toolRoleMap={toolRoleMap} systemPrompt={run.system_prompt} runId={id!} runStatus={run.status} />

              {hasMore && (
                <button
                  type="button"
                  className={styles.loadMoreBtn}
                  onClick={() => setDisplayedCount((c) => c + PAGE_SIZE)}
                >
                  Load more ({filteredSteps.length - displayedCount} remaining)
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
