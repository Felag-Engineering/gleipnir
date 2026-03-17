import { Link, useSearchParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { useRuns } from '@/hooks/useRuns'
import { usePolicies } from '@/hooks/usePolicies'
import { queryKeys } from '@/hooks/queryKeys'
import SkeletonBlock from '@/components/SkeletonBlock/SkeletonBlock'
import { StatusBadge } from '@/components/dashboard/StatusBadge'
import { TriggerChip } from '@/components/dashboard/TriggerChip'
import type { RunStatus, TriggerType } from '@/components/dashboard/types'
import { fmtRel, fmtTok, fmtDur, fmtAbs } from '@/components/dashboard/styles'
import type { ApiRun } from '@/api/types'
import styles from './RunsPage.module.css'

const PAGE_SIZE = 25

const KNOWN_STATUSES = new Set<string>([
  'complete', 'running', 'waiting_for_approval', 'failed', 'interrupted', 'pending',
])
const KNOWN_TRIGGERS = new Set<string>(['webhook', 'cron', 'poll', 'manual'])

function computeDuration(run: ApiRun): number | null {
  if (!run.completed_at) return null
  return Math.floor(
    (new Date(run.completed_at).getTime() - new Date(run.started_at).getTime()) / 1000,
  )
}

function rangeToSince(range: string): string | undefined {
  const now = Date.now()
  if (range === '1h') return new Date(now - 3_600_000).toISOString()
  if (range === '24h') return new Date(now - 86_400_000).toISOString()
  if (range === '7d') return new Date(now - 7 * 86_400_000).toISOString()
  return undefined
}

export default function RunsPage() {
  const queryClient = useQueryClient()
  const [searchParams, setSearchParams] = useSearchParams()

  const status = searchParams.get('status') ?? ''
  const policy = searchParams.get('policy') ?? ''
  const range = searchParams.get('range') ?? 'all'
  const sort = searchParams.get('sort') ?? 'started'
  const order = searchParams.get('order') ?? 'desc'
  const page = Math.max(1, parseInt(searchParams.get('page') ?? '1', 10))

  const since = range !== 'all' ? rangeToSince(range) : undefined
  const offset = (page - 1) * PAGE_SIZE

  const { data, status: fetchStatus } = useRuns({
    status: status || undefined,
    policy_id: policy || undefined,
    since,
    sort,
    order,
    limit: PAGE_SIZE,
    offset,
  })

  const { data: policies } = usePolicies()

  function setFilter(key: string, value: string) {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev)
      if (value) {
        next.set(key, value)
      } else {
        next.delete(key)
      }
      next.delete('page')
      return next
    })
  }

  function toggleSort(field: string) {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev)
      const currentSort = prev.get('sort') ?? 'started'
      const currentOrder = prev.get('order') ?? 'desc'
      if (currentSort === field) {
        next.set('order', currentOrder === 'desc' ? 'asc' : 'desc')
      } else {
        next.set('sort', field)
        next.set('order', 'desc')
      }
      next.delete('page')
      return next
    })
  }

  const runs = data?.runs ?? []
  const total = data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const firstItem = offset + 1
  const lastItem = Math.min(offset + runs.length, total)

  function sortIndicator(field: string) {
    if (sort !== field) return ''
    return order === 'asc' ? ' ▲' : ' ▼'
  }

  function renderContent() {
    if (fetchStatus === 'pending') {
      return (
        <div className={styles.skeletonList}>
          <SkeletonBlock height={48} />
          <SkeletonBlock height={48} />
          <SkeletonBlock height={48} />
          <SkeletonBlock height={48} />
          <SkeletonBlock height={48} />
        </div>
      )
    }

    if (fetchStatus === 'error') {
      return (
        <div className={styles.errorState} role="alert">
          <span>Failed to load runs.</span>
          <button
            className={styles.retryBtn}
            onClick={() => queryClient.invalidateQueries({ queryKey: queryKeys.runs.all })}
          >
            Retry
          </button>
        </div>
      )
    }

    if (runs.length === 0) {
      return (
        <div className={styles.errorState}>
          <span>No runs found.</span>
          <span>Try adjusting the filters, or trigger a policy to create a run.</span>
        </div>
      )
    }

    return (
      <div className={styles.table}>
        <div className={styles.headerRow}>
          <span>Run ID</span>
          <span>Policy</span>
          <span>Status</span>
          <span>Trigger</span>
          <span
            className={`${styles.sortable} ${sort === 'started' ? styles.sortActive : ''}`}
            onClick={() => toggleSort('started')}
            role="button"
            tabIndex={0}
            onKeyDown={(e) => e.key === 'Enter' && toggleSort('started')}
          >
            Started{sortIndicator('started')}
          </span>
          <span>Duration</span>
          <span>Tokens</span>
          <span>Error</span>
        </div>
        {runs.map((run) => (
          <Link key={run.id} to={`/runs/${run.id}`} className={styles.row}>
            <span className={styles.runId} title={run.id}>
              {run.id.slice(0, 8)}
            </span>
            <span className={styles.policyName} title={run.policy_name ?? run.policy_id}>
              {run.policy_name || run.policy_id}
            </span>
            <span>
              {KNOWN_STATUSES.has(run.status) && (
                <StatusBadge status={run.status as RunStatus} />
              )}
            </span>
            <span>
              {KNOWN_TRIGGERS.has(run.trigger_type) && (
                <TriggerChip type={run.trigger_type as TriggerType} />
              )}
            </span>
            <span className={styles.mono} title={fmtAbs(run.started_at)}>
              {fmtRel(run.started_at)}
            </span>
            <span className={styles.mono}>{fmtDur(computeDuration(run))}</span>
            <span className={styles.tokensCell}>{fmtTok(run.token_cost)}</span>
            <span className={styles.errorCell} title={run.error ?? undefined}>
              {run.error ?? ''}
            </span>
          </Link>
        ))}
      </div>
    )
  }

  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <h1 className={styles.title}>Runs</h1>
      </div>

      <div className={styles.filters}>
        <select
          className={styles.filterSelect}
          value={status}
          onChange={(e) => setFilter('status', e.target.value)}
          aria-label="Filter by status"
        >
          <option value="">All statuses</option>
          <option value="complete">Complete</option>
          <option value="running">Running</option>
          <option value="failed">Failed</option>
          <option value="pending">Pending</option>
          <option value="waiting_for_approval">Waiting for approval</option>
          <option value="interrupted">Interrupted</option>
        </select>

        <select
          className={styles.filterSelect}
          value={policy}
          onChange={(e) => setFilter('policy', e.target.value)}
          aria-label="Filter by policy"
        >
          <option value="">All policies</option>
          {(policies ?? []).map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>

        <select
          className={styles.filterSelect}
          value={range}
          onChange={(e) => setFilter('range', e.target.value)}
          aria-label="Filter by date range"
        >
          <option value="all">All time</option>
          <option value="1h">Last hour</option>
          <option value="24h">Last 24 hours</option>
          <option value="7d">Last 7 days</option>
        </select>
      </div>

      {renderContent()}

      {fetchStatus === 'success' && total > 0 && (
        <div className={styles.pagination}>
          <span className={styles.pageInfo}>
            Showing {firstItem}–{lastItem} of {total} runs
          </span>
          <div>
            <button
              className={styles.pageBtn}
              disabled={page <= 1}
              onClick={() =>
                setSearchParams((prev) => {
                  const next = new URLSearchParams(prev)
                  next.set('page', String(page - 1))
                  return next
                })
              }
            >
              Previous
            </button>{' '}
            <button
              className={styles.pageBtn}
              disabled={page >= totalPages}
              onClick={() =>
                setSearchParams((prev) => {
                  const next = new URLSearchParams(prev)
                  next.set('page', String(page + 1))
                  return next
                })
              }
            >
              Next
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
