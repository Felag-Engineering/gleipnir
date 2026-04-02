import { Link, useSearchParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { useRuns } from '@/hooks/useRuns'
import { usePolicies } from '@/hooks/usePolicies'
import { queryKeys } from '@/hooks/queryKeys'
import { QueryBoundary } from '@/components/QueryBoundary'
import { StatusBadge } from '@/components/dashboard/StatusBadge'
import EmptyState from '@/components/EmptyState/EmptyState'
import type { RunStatus } from '@/constants/status'
import { formatTimeAgo, formatTokens, formatDuration, formatTimestamp, computeRunDuration } from '@/utils/format'
import { usePageTitle } from '@/hooks/usePageTitle'
import styles from './RunsPage.module.css'

const PAGE_SIZE = 25

// Status filter definitions for the chip bar
const STATUS_FILTERS = [
  { key: '', label: 'All' },
  { key: 'complete', label: 'Complete' },
  { key: 'running', label: 'Running' },
  { key: 'failed', label: 'Failed' },
  { key: 'waiting_for_approval', label: 'Approval' },
] as const

// Date range options for the secondary filter chip
const DATE_RANGES = [
  { key: 'all', label: 'All time' },
  { key: '1h', label: 'Last hour' },
  { key: '24h', label: 'Last 24h' },
  { key: '7d', label: 'Last 7 days' },
] as const

function rangeToSince(range: string): string | undefined {
  const now = Date.now()
  if (range === '1h') return new Date(now - 3_600_000).toISOString()
  if (range === '24h') return new Date(now - 86_400_000).toISOString()
  if (range === '7d') return new Date(now - 7 * 86_400_000).toISOString()
  return undefined
}

// Returns page numbers to display, inserting 'ellipsis' for gaps.
// Always shows first and last page, current page ±1, with ellipsis between non-adjacent groups.
export function computePageNumbers(currentPage: number, totalPages: number): (number | 'ellipsis')[] {
  if (totalPages <= 1) return [1]

  // For small page counts, show all pages with no ellipsis needed
  if (totalPages <= 7) {
    return Array.from({ length: totalPages }, (_, i) => i + 1)
  }

  const result: (number | 'ellipsis')[] = []

  // The window of pages to always show: current ±1
  const windowStart = Math.max(2, currentPage - 1)
  const windowEnd = Math.min(totalPages - 1, currentPage + 1)

  result.push(1)

  if (windowStart > 2) {
    result.push('ellipsis')
  }

  for (let p = windowStart; p <= windowEnd; p++) {
    result.push(p)
  }

  if (windowEnd < totalPages - 1) {
    result.push('ellipsis')
  }

  result.push(totalPages)

  return result
}

// Maps a run status to the stripe CSS class that colors the left border bar.
function getStripeClass(status: string): string {
  const map: Record<string, string> = {
    complete: styles.stripeComplete,
    running: styles.stripeRunning,
    failed: styles.stripeFailed,
    waiting_for_approval: styles.stripeApproval,
    waiting_for_feedback: styles.stripeApproval,
    interrupted: styles.stripeInterrupted,
    pending: styles.stripePending,
  }
  return map[status] ?? styles.stripePending
}

// Maps a run status to the row background CSS class.
function getRowBgClass(status: string): string {
  const map: Record<string, string> = {
    complete: styles.rowComplete,
    running: styles.rowRunning,
    failed: styles.rowFailed,
    waiting_for_approval: styles.rowApproval,
    waiting_for_feedback: styles.rowApproval,
    interrupted: styles.rowInterrupted,
    pending: styles.rowPending,
  }
  return map[status] ?? styles.rowComplete
}

export default function RunsPage() {
  const queryClient = useQueryClient()
  const [searchParams, setSearchParams] = useSearchParams()

  const status = searchParams.get('status') ?? ''
  const policy = searchParams.get('policy') ?? ''
  const range = searchParams.get('range') ?? 'all'
  // 'sort' encodes direction as 'newest' (default) or 'oldest'
  const sort = searchParams.get('sort') ?? 'newest'
  const page = Math.max(1, parseInt(searchParams.get('page') ?? '1', 10))

  // Map the sort chip value to the API's sort/order params
  const apiSort = 'started'
  const apiOrder = sort === 'oldest' ? 'asc' : 'desc'

  const since = range !== 'all' ? rangeToSince(range) : undefined
  const offset = (page - 1) * PAGE_SIZE

  const { runs, total, status: fetchStatus } = useRuns({
    status: status || undefined,
    policy_id: policy || undefined,
    since,
    sort: apiSort,
    order: apiOrder,
    limit: PAGE_SIZE,
    offset,
  })

  usePageTitle('Run History')
  const { data: policies } = usePolicies()

  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const firstItem = offset + 1
  const lastItem = Math.min(offset + runs.length, total)
  const pageNumbers = computePageNumbers(page, totalPages)

  // Stats subtitle — deferred mode shows total count since backend doesn't
  // yet return per-status counts or token sums in the stats field.
  const statsLine = total > 0 ? `${total} runs` : ''

  function setFilter(key: string, value: string) {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev)
      if (value) {
        next.set(key, value)
      } else {
        next.delete(key)
      }
      // Reset to page 1 whenever a filter changes
      next.delete('page')
      return next
    })
  }

  function goToPage(p: number) {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev)
      next.set('page', String(p))
      return next
    })
  }

  function toggleSort() {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev)
      const current = prev.get('sort') ?? 'newest'
      next.set('sort', current === 'newest' ? 'oldest' : 'newest')
      next.delete('page')
      return next
    })
  }

  const sortLabel = sort === 'oldest' ? 'Oldest ▲' : 'Newest ▼'
  const sortAriaLabel =
    sort === 'oldest' ? 'Sort by date, currently oldest first' : 'Sort by date, currently newest first'

  const selectedDateLabel = DATE_RANGES.find((d) => d.key === range)?.label ?? 'All time'
  const selectedPolicyName = policies?.find((p) => p.id === policy)?.name

  return (
    <div className={styles.page}>
      {/* Title row with contextual stats on the right */}
      <div className={styles.titleRow}>
        <h1 className={styles.titleText}>Run History</h1>
        {fetchStatus === 'success' && statsLine && (
          <span className={styles.titleStats}>{statsLine}</span>
        )}
      </div>

      {/* Filter bar: status chip group | divider | policy + date selects | sort chip */}
      <div className={styles.filterBar}>
        <div role="radiogroup" aria-label="Filter by status" className={styles.statusGroup}>
          {STATUS_FILTERS.map(({ key, label }) => {
            const isActive = status === key
            return (
              <button
                key={key}
                role="radio"
                aria-checked={isActive}
                className={isActive ? `${styles.chip} ${styles.chipActive}` : styles.chip}
                onClick={() => setFilter('status', key)}
              >
                {label}
              </button>
            )
          })}
        </div>

        <div className={styles.filterDivider} aria-hidden="true" />

        {/* Policy filter: styled native select to get accessibility for free */}
        <select
          className={policy ? `${styles.chip} ${styles.chipActive}` : styles.chip}
          value={policy}
          onChange={(e) => setFilter('policy', e.target.value)}
          aria-label="Filter by policy"
        >
          <option value="">All policies ▾</option>
          {(policies ?? []).map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>

        {/* Date range filter: styled native select */}
        <select
          className={range !== 'all' ? `${styles.chip} ${styles.chipActive}` : styles.chip}
          value={range}
          onChange={(e) => setFilter('range', e.target.value)}
          aria-label="Filter by date range"
        >
          {DATE_RANGES.map(({ key, label }) => (
            <option key={key} value={key}>
              {label} ▾
            </option>
          ))}
        </select>

        {/* Sort chip — pushed to far right via margin-left: auto in CSS */}
        <button
          className={styles.sortChip}
          onClick={toggleSort}
          aria-label={sortAriaLabel}
        >
          {sortLabel}
        </button>
      </div>

      <QueryBoundary
        status={fetchStatus}
        isEmpty={runs.length === 0}
        errorMessage="Failed to load runs."
        onRetry={() => queryClient.invalidateQueries({ queryKey: queryKeys.runs.all })}
        emptyState={
          <EmptyState
            headline="No runs found"
            subtext="Try adjusting the filters, or go to Policies to trigger a run."
            ctaLabel="Go to Policies"
            ctaTo="/policies"
          />
        }
      >
        <div className={styles.runList}>
          {runs.map((run) => (
            <Link
              key={run.id}
              to={`/runs/${run.id}`}
              className={`${styles.row} ${getRowBgClass(run.status)}`}
            >
              {/* Left stripe encodes status visually; badge communicates it in text */}
              <div className={`${styles.stripe} ${getStripeClass(run.status)}`} aria-hidden="true" />

              {/* Policy name (primary) + run ID and trigger type (secondary) */}
              <div className={styles.identity}>
                <div className={styles.policyName} title={run.policy_name ?? run.policy_id}>
                  {run.policy_name || run.policy_id}
                </div>
                <div className={styles.subtext}>
                  {run.id.slice(0, 8)} · {run.trigger_type}
                </div>
              </div>

              <StatusBadge status={run.status as RunStatus} />

              <span className={styles.duration}>
                {formatDuration(computeRunDuration(run))}
              </span>

              <div className={styles.timeTokens}>
                <div className={styles.timeAgo} title={formatTimestamp(run.started_at)}>
                  {formatTimeAgo(run.started_at)}
                </div>
                <div className={styles.tokens}>{formatTokens(run.token_cost)} tok</div>
              </div>

              <span className={styles.arrow} aria-hidden="true">›</span>
            </Link>
          ))}
        </div>
      </QueryBoundary>

      {/* Pagination — only shown when there are runs to paginate */}
      {fetchStatus === 'success' && total > 0 && (
        <div className={styles.pagination}>
          <span className={styles.pageInfo}>
            {firstItem}–{lastItem} of {total}
          </span>
          <div className={styles.pageButtons}>
            <button
              disabled={page <= 1}
              aria-label="Previous page"
              className={`${styles.pageButton} ${page <= 1 ? styles.pageButtonDisabled : ''}`}
              onClick={() => goToPage(page - 1)}
            >
              ←
            </button>

            {pageNumbers.map((n, i) =>
              n === 'ellipsis' ? (
                <span key={`e${i}`} className={styles.pageEllipsis}>
                  …
                </span>
              ) : (
                <button
                  key={n}
                  aria-label={`Page ${n}`}
                  aria-current={n === page ? 'page' : undefined}
                  className={`${styles.pageButton} ${n === page ? styles.pageButtonActive : ''}`}
                  onClick={() => goToPage(n)}
                >
                  {n}
                </button>
              )
            )}

            <button
              disabled={page >= totalPages}
              aria-label="Next page"
              className={`${styles.pageButton} ${page >= totalPages ? styles.pageButtonDisabled : ''}`}
              onClick={() => goToPage(page + 1)}
            >
              →
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
