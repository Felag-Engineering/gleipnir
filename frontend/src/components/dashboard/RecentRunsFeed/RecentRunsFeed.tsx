import { Link, useNavigate } from 'react-router-dom'
import { useRuns } from '@/hooks/useRuns'
import { StatusBadge } from '@/components/dashboard/StatusBadge'
import { EmptyState } from '@/components/EmptyState'
import { SkeletonBlock } from '@/components/SkeletonBlock'
import type { RunStatus } from '@/components/dashboard/types'
import {
  formatTimeAgo,
  formatTokens,
  formatDuration,
  computeRunDuration,
} from '@/utils/format'
import styles from './RecentRunsFeed.module.css'

export function RecentRunsFeed() {
  const { runs, isLoading } = useRuns({ limit: 10, sort: 'started_at', order: 'desc' })
  const navigate = useNavigate()

  return (
    <div>
      <div className={styles.header}>
        <span className={styles.title}>RECENT RUNS</span>
        <Link to="/runs" className={styles.viewAll}>
          View all runs →
        </Link>
      </div>

      <div className={styles.container}>
        {isLoading && (
          <>
            {Array.from({ length: 5 }).map((_, i) => (
              <div key={i} className={styles.skeletonRow}>
                <SkeletonBlock height={16} width="40%" />
                <SkeletonBlock height={16} width={80} />
                <SkeletonBlock height={16} width={48} />
                <SkeletonBlock height={16} width={48} />
                <SkeletonBlock height={16} width={64} />
              </div>
            ))}
          </>
        )}

        {!isLoading && runs.length === 0 && (
          <EmptyState
            headline="No runs yet"
            subtext="Create a policy and trigger your first run."
            ctaLabel="Go to Policies"
            ctaTo="/policies"
          />
        )}

        {!isLoading && runs.map((run, idx) => {
          const duration = computeRunDuration(run)
          return (
            <div
              key={run.id}
              className={`${styles.row} ${idx < runs.length - 1 ? styles.rowBorder : ''}`}
              onClick={() => navigate(`/runs/${run.id}`)}
              role="button"
              tabIndex={0}
              onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') navigate(`/runs/${run.id}`) }}
            >
              <span className={styles.colPolicy}>
                {run.policy_name || run.policy_id}
              </span>
              <span className={styles.colStatus}>
                <StatusBadge status={run.status as RunStatus} />
              </span>
              <span className={styles.colDuration}>
                {formatDuration(duration)}
              </span>
              <span className={styles.colTokens}>
                {formatTokens(run.token_cost)}
              </span>
              <span className={styles.colTime}>
                {formatTimeAgo(run.created_at)}
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}
