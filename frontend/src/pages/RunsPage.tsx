import { Link } from 'react-router-dom'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { useRuns } from '@/hooks/queries/runs'
import { usePolicies } from '@/hooks/queries/policies'
import { useRunsFilters } from '@/hooks/useRunsFilters'
import { queryKeys } from '@/hooks/queryKeys'
import { QueryBoundary } from '@/components/QueryBoundary'
import { StatusBadge } from '@/components/dashboard/StatusBadge'
import EmptyState from '@/components/EmptyState/EmptyState'
import type { RunStatus } from '@/constants/status'
import { formatTimeAgo, formatTokens, formatDuration, formatTimestamp, computeRunDuration } from '@/utils/format'
import { computePageNumbers, rangeToSince } from '@/utils/pagination'
import { usePageTitle } from '@/hooks/usePageTitle'
import { getRunRowClasses } from './runsUtils'
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

export default function RunsPage() {
  const queryClient = useQueryClient()
  const { status, policy, range, sort, page, setFilter, goToPage, toggleSort } = useRunsFilters()

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
  const statsLine = total > 0 ? `${total} ${total === 1 ? 'run' : 'runs'}` : ''

  const sortLabel = sort === 'oldest' ? 'Oldest ▲' : 'Newest ▼'
  const sortAriaLabel =
    sort === 'oldest' ? 'Sort by date, currently oldest first' : 'Sort by date, currently newest first'

  return (
    <div className={styles.page}>
      {/* Title row with contextual stats on the right */}
      <div className={styles.titleRow}>
        <h1 className={styles.titleText}>Run History</h1>
        {fetchStatus === 'success' && statsLine && (
          <span className={styles.titleStats}>{statsLine}</span>
        )}
      </div>

      {/* Filter bar: status chip group | divider | agent + date selects | sort chip */}
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

        {/* Agent filter: styled native select to get accessibility for free */}
        <select
          className={policy ? `${styles.chip} ${styles.chipActive}` : styles.chip}
          value={policy}
          onChange={(e) => setFilter('policy', e.target.value)}
          aria-label="Filter by agent"
        >
          <option value="">All agents ▾</option>
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
            subtext="Try adjusting the filters, or go to Agents to trigger a run."
            ctaLabel="Go to Agents"
            ctaTo="/agents"
          />
        }
      >
        <div className={styles.runList}>
          {runs.map((run) => {
            const { stripe, rowBg } = getRunRowClasses(run.status)
            return (
              <Link
                key={run.id}
                to={`/runs/${run.id}`}
                className={`${styles.row} ${rowBg}`}
              >
                {/* Left stripe encodes status visually; badge communicates it in text */}
                <div className={`${styles.stripe} ${stripe}`} aria-hidden="true" />

                {/* Agent name (primary) + run ID and trigger type (secondary) */}
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
            )
          })}
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
              <ChevronLeft size={16} aria-hidden strokeWidth={1.5} />
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
              <ChevronRight size={16} aria-hidden strokeWidth={1.5} />
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
