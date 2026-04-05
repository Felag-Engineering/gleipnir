import { useState } from 'react'
import { Link } from 'react-router-dom'
import type { ApiPolicyListItem } from '@/api/types'
import { StatusBadge } from '@/components/dashboard/StatusBadge'
import { isRunStatus } from '@/constants/status'
import type { RunStatus } from '@/constants/status'
import { formatTimeAgo } from '@/utils/format'
import { PolicyCardExpanded } from './PolicyCardExpanded'
import styles from './PolicyCard.module.css'

interface Props {
  policy: ApiPolicyListItem
  onTrigger: (policyId: string, policyName: string) => void
}

function statusDotClass(status: string | undefined): string {
  if (!status) return styles.dotNone
  switch (status) {
    case 'complete': return styles.dotComplete
    case 'failed': return styles.dotFailed
    case 'running': return styles.dotRunning
    case 'waiting_for_approval':
    case 'waiting_for_feedback': return styles.dotWaiting
    default: return styles.dotNone
  }
}

export function PolicyCard({ policy, onTrigger }: Props) {
  const [expanded, setExpanded] = useState(false)
  const run = policy.latest_run

  return (
    <div className={`${styles.card} ${expanded ? styles.cardExpanded : ''}`}>
      <div
        className={styles.collapsed}
        onClick={() => setExpanded(!expanded)}
        role="button"
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            setExpanded(!expanded)
          }
        }}
      >
        <div className={styles.leftZone}>
          <div className={`${styles.statusDot} ${statusDotClass(run?.status)}`} />
          <span className={styles.policyName}>{policy.name}</span>
          <span className={styles.triggerPill}>{policy.trigger_type}</span>
          {policy.model && <span className={styles.modelPill}>{policy.model}</span>}
          {policy.tool_count > 0 && (
            <span className={styles.toolCount}>{policy.tool_count} tools</span>
          )}
        </div>
        <div className={styles.rightZone}>
          {run && isRunStatus(run.status) && (
            <>
              <StatusBadge status={run.status as RunStatus} />
              <span className={styles.timeAgo}>{formatTimeAgo(run.started_at)}</span>
            </>
          )}
          <button
            className={`${styles.actionBtn} ${styles.playBtn}`}
            onClick={(e) => { e.stopPropagation(); onTrigger(policy.id, policy.name) }}
            aria-label={`Run ${policy.name}`}
          >
            ▶
          </button>
          <Link
            to={`/agents/${policy.id}`}
            className={`${styles.actionBtn} ${styles.editBtn}`}
            onClick={(e) => e.stopPropagation()}
            aria-label={`Edit ${policy.name}`}
          >
            ✎
          </Link>
        </div>
      </div>
      {expanded && <PolicyCardExpanded policy={policy} />}
    </div>
  )
}
