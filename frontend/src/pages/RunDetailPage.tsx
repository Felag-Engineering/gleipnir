import { useState } from 'react'
import { useParams } from 'react-router-dom'
import { useRun } from '@/hooks/queries/runs'
import { useRunSteps } from '@/hooks/queries/runs'
import { useRunTimeline } from '@/hooks/useRunTimeline'
import { useScrollSentinel } from '@/hooks/useScrollSentinel'
import SkeletonBlock from '@/components/SkeletonBlock/SkeletonBlock'
import { QueryBoundary } from '@/components/QueryBoundary'
import { EmptyState } from '@/components/EmptyState'
import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import {
  RunHeader,
  FilterBar,
  StepTimeline,
} from '@/components/RunDetail'
import type { FilterKey } from '@/components/RunDetail'
import { usePageTitle } from '@/hooks/usePageTitle'
import styles from './RunDetailPage.module.css'

export default function RunDetailPage() {
  const { id } = useParams<{ id: string }>()
  const { data: run, status: runStatus, refetch: runRefetch } = useRun(id)
  const { data: rawSteps = [], status: stepsStatus } = useRunSteps(id)

  usePageTitle(id ? `Run ${id.slice(0, 8)}` : 'Run')
  const [filter, setFilter] = useState<FilterKey>('all')

  const { timelineItems, counts, hasMore, remainingCount, loadMore } = useRunTimeline(rawSteps, filter)
  const { sentinelRef, showNewPill, scrollToBottom } = useScrollSentinel(rawSteps.length)

  // Duration computation
  const duration =
    run
      ? run.completed_at
        ? new Date(run.completed_at).getTime() - new Date(run.started_at).getTime()
        : Date.now() - new Date(run.started_at).getTime()
      : null
  const durationSeconds = duration !== null ? duration / 1000 : null

  const toolCallCount = rawSteps.filter((s) => s.type === 'tool_call').length
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
                  onClick={loadMore}
                >
                  Load more ({remainingCount} remaining)
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
