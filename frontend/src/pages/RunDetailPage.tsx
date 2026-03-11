import { useState, useRef, useEffect, useCallback } from 'react'
import { useParams } from 'react-router-dom'
import { useRun } from '@/hooks/useRun'
import { useRunSteps } from '@/hooks/useRunSteps'
import SkeletonBlock from '@/components/SkeletonBlock/SkeletonBlock'
import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import {
  RunHeader,
  MetadataGrid,
  FilterBar,
  StepTimeline,
  parseStep,
} from '@/components/RunDetail'
import type { FilterKey } from '@/components/RunDetail'
import type { ParsedStep, CapabilitySnapshotContent, GrantedToolEntry } from '@/components/RunDetail/types'
import styles from './RunDetailPage.module.css'

const PAGE_SIZE = 50

export default function RunDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { data: run, status: runStatus } = useRun(id)
  const { data: rawSteps = [], status: stepsStatus } = useRunSteps(id)

  const [filter, setFilter] = useState<FilterKey>('all')
  const [displayedCount, setDisplayedCount] = useState(PAGE_SIZE)
  const [showNewPill, setShowNewPill] = useState(false)
  const [isNearBottom, setIsNearBottom] = useState(true)

  const sentinelRef = useRef<HTMLDivElement>(null)
  const prevStepCount = useRef(0)

  // Parse all steps once
  const allParsed: ParsedStep[] = rawSteps.map(parseStep)

  // Build tool role map from capability_snapshot steps
  const toolRoleMap = new Map<string, GrantedToolEntry['Role']>()
  for (const step of allParsed) {
    if (step.type === 'capability_snapshot') {
      const entries = step.content as CapabilitySnapshotContent
      if (Array.isArray(entries)) {
        for (const entry of entries) {
          toolRoleMap.set(entry.ToolName, entry.Role)
        }
      }
    }
  }

  // Separate capability_snapshot steps; filter counts exclude them (ADR-018)
  const nonSnapshotSteps = allParsed.filter((s) => s.type !== 'capability_snapshot')
  const snapshotSteps = allParsed.filter((s) => s.type === 'capability_snapshot')

  // Filter counts
  const counts: Record<FilterKey, number> = {
    all: nonSnapshotSteps.length,
    thought: nonSnapshotSteps.filter((s) => s.type === 'thought').length,
    tool_call: nonSnapshotSteps.filter((s) => s.type === 'tool_call').length,
    tool_result: nonSnapshotSteps.filter((s) => s.type === 'tool_result').length,
    error: nonSnapshotSteps.filter((s) => s.type === 'error').length,
  }

  // Apply filter
  const filteredSteps =
    filter === 'all'
      ? nonSnapshotSteps
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

  return (
    <div className={styles.page}>
      {isLoading && (
        <div className={styles.skeleton}>
          <SkeletonBlock height={48} />
          <SkeletonBlock height={24} width="60%" />
          <SkeletonBlock height={80} />
          <SkeletonBlock height={40} />
          <SkeletonBlock height={120} />
          <SkeletonBlock height={120} />
        </div>
      )}

      {!isLoading && run && (
        <>
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
            <StepTimeline steps={timelineSteps} toolRoleMap={toolRoleMap} />

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
        </>
      )}
    </div>
  )
}
