import { Link } from 'react-router-dom'
import type { ApiRun } from '@/api/types'
import { StatusBadge } from '../StatusBadge'
import type { RunStatus } from '../types'
import { fmtRel } from '../styles'
import { SkeletonBlock } from '@/components/SkeletonBlock'
import styles from './ActivityFeed.module.css'

interface ActivityFeedProps {
  runs: ApiRun[]
  isLoading: boolean
}

export function ActivityFeed({ runs, isLoading }: ActivityFeedProps) {
  if (isLoading) {
    return (
      <div className={styles.feed}>
        <div className={styles.sectionTitle}>Recent Activity</div>
        {Array.from({ length: 5 }).map((_, i) => (
          <div key={i} className={styles.entry}>
            <SkeletonBlock height={20} />
          </div>
        ))}
      </div>
    )
  }

  if (runs.length === 0) {
    return (
      <div className={styles.feed}>
        <div className={styles.sectionTitle}>Recent Activity</div>
        <div className={styles.empty}>No recent activity</div>
      </div>
    )
  }

  return (
    <div className={styles.feed}>
      <div className={styles.sectionTitle}>Recent Activity</div>
      {runs.slice(0, 20).map(run => (
        <div key={run.id} className={styles.entry}>
          <Link to={`/runs/${run.id}`} className={styles.entryLink}>
            <span className={styles.policyName}>
              {run.policy_name || run.policy_id}
            </span>
            <StatusBadge status={run.status as RunStatus} />
            <span className={styles.timestamp}>{fmtRel(run.created_at)}</span>
          </Link>
        </div>
      ))}
    </div>
  )
}
