import { Link } from 'react-router-dom'
import type { ApiPolicyListItem } from '@/api/types'
import { StatusBadge } from '../StatusBadge'
import type { RunStatus } from '../types'
import { TriggerChip } from '../TriggerChip'
import type { TriggerType } from '../types'
import { fmtRel } from '../styles'
import styles from './StatusBoard.module.css'

interface StatusBoardProps {
  policies: ApiPolicyListItem[]
  onTrigger: (id: string, name: string) => void
}

export function StatusBoard({ policies, onTrigger: _onTrigger }: StatusBoardProps) {
  if (policies.length === 0) {
    return (
      <div className={styles.board}>
        <div className={styles.sectionTitle}>Policy Status</div>
        <div className={styles.idle}>No policies</div>
      </div>
    )
  }

  // Waiting-for-approval policies sort to top
  const sorted = [...policies].sort((a, b) => {
    const aWaiting = a.latest_run?.status === 'waiting_for_approval' ? 0 : 1
    const bWaiting = b.latest_run?.status === 'waiting_for_approval' ? 0 : 1
    return aWaiting - bWaiting
  })

  return (
    <div className={styles.board}>
      <div className={styles.sectionTitle}>Policy Status</div>
      {sorted.map(policy => {
        const isWaiting = policy.latest_run?.status === 'waiting_for_approval'
        return (
          <div
            key={policy.id}
            className={`${styles.row}${isWaiting ? ` ${styles.rowApproval}` : ''}`}
          >
            <Link to={`/policies/${policy.id}/runs`} className={styles.policyName}>
              {policy.name}
            </Link>
            <TriggerChip type={policy.trigger_type as TriggerType} pausedAt={policy.paused_at} />
            {policy.latest_run ? (
              <StatusBadge status={policy.latest_run.status as RunStatus} />
            ) : (
              <span className={styles.idle}>never run</span>
            )}
            {isWaiting && policy.latest_run ? (
              <Link to={`/runs/${policy.latest_run.id}`} className={styles.reviewLink}>
                Review →
              </Link>
            ) : policy.latest_run ? (
              <span className={styles.lastRun}>{fmtRel(policy.latest_run.started_at)}</span>
            ) : (
              <span />
            )}
          </div>
        )
      })}
    </div>
  )
}
