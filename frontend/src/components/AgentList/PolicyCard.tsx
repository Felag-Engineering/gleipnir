import { useState } from 'react'
import { Link } from 'react-router-dom'
import { Play, Pencil } from 'lucide-react'
import type { ApiPolicyListItem } from '@/api/types'
import { StatusBadge } from '@/components/dashboard/StatusBadge'
import { isRunStatus } from '@/constants/status'
import type { RunStatus } from '@/constants/status'
import { formatTimeAgo, formatTimestamp } from '@/utils/format'
import { PolicyCardExpanded } from './PolicyCardExpanded'
import styles from './PolicyCard.module.css'

// formatNextFire returns a relative "in Xm" / "in Xh" string for a future ISO
// timestamp, or null if the timestamp is in the past or unparseable.
function formatNextFire(iso: string): string | null {
  try {
    const diffMs = new Date(iso).getTime() - Date.now()
    if (diffMs <= 0) return null
    const m = Math.floor(diffMs / 60000)
    if (m < 1) return 'in <1m'
    if (m < 60) return `in ${m}m`
    const h = Math.floor(m / 60)
    if (h < 24) return `in ${h}h`
    // For times > 24h out, fall back to absolute timestamp for clarity
    return formatTimestamp(iso)
  } catch {
    return null
  }
}

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
  const isPaused = Boolean(policy.paused_at)
  const isTimedTrigger = policy.trigger_type === 'scheduled' || policy.trigger_type === 'poll'
  const nextFireLabel = isTimedTrigger && policy.next_fire_at ? formatNextFire(policy.next_fire_at) : null

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
          {isPaused && <span className={styles.pausedPill}>Paused</span>}
          {policy.model && <span className={styles.modelPill}>{policy.model}</span>}
          {policy.tool_count > 0 && (
            <span className={styles.toolCount}>{policy.tool_count} {policy.tool_count === 1 ? 'tool' : 'tools'}</span>
          )}
        </div>
        <div className={styles.rightZone}>
          {nextFireLabel && (
            <span className={styles.nextFire}>Next: {nextFireLabel}</span>
          )}
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
            disabled={isPaused}
          >
            <Play size={12} aria-hidden />
          </button>
          <Link
            to={`/agents/${policy.id}`}
            className={`${styles.actionBtn} ${styles.editBtn}`}
            onClick={(e) => e.stopPropagation()}
            aria-label={`Edit ${policy.name}`}
          >
            <Pencil size={14} aria-hidden />
          </Link>
        </div>
      </div>
      {expanded && <PolicyCardExpanded policy={policy} />}
    </div>
  )
}
