import { Link } from 'react-router-dom'
import type { ApiPolicyListItem } from '../../../api/types'
import type { RunStatus, TriggerType } from '../types'
import { fmtDur, fmtTok, fmtRel } from '../styles'
import { StatusBadge } from '../StatusBadge'
import { TriggerChip } from '../TriggerChip'
import styles from './PolicyList.module.css'

interface PolicyListProps {
  policies: ApiPolicyListItem[]
}

const KNOWN_STATUSES: RunStatus[] = [
  'complete', 'running', 'waiting_for_approval', 'failed', 'interrupted', 'pending',
]

function isRunStatus(s: string): s is RunStatus {
  return (KNOWN_STATUSES as string[]).includes(s)
}

function RunCell({ policy }: { policy: ApiPolicyListItem }) {
  const run = policy.latest_run
  if (!run) {
    return <span className={styles.muted}>—</span>
  }

  const status = isRunStatus(run.status) ? run.status : 'failed'
  const isRunning = status === 'running'

  return (
    <Link to={`/runs/${run.id}`} className={styles.runLink}>
      <StatusBadge status={status} />
      {isRunning ? (
        <span className={styles.executingRow}>
          <svg className={styles.spinner} viewBox="0 0 16 16" fill="none" aria-hidden="true">
            <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="2" strokeDasharray="28" strokeDashoffset="10" />
          </svg>
          <span className={styles.executingText}>Executing…</span>
        </span>
      ) : null}
    </Link>
  )
}

export function PolicyList({ policies }: PolicyListProps) {
  return (
    <div className={styles.tableWrapper}>
      <div className={styles.table}>
        <div className={`${styles.row} ${styles.headerRow}`}>
          <span className={styles.colPolicy}>Policy</span>
          <span className={styles.colStatus}>Status</span>
          <span className={styles.colWhen}>When</span>
          <span className={styles.colDuration}>Duration</span>
          <span className={styles.colTokens}>Tokens</span>
        </div>

        {policies.map((policy) => {
          const run = policy.latest_run
          // trigger_type: API is authoritative, cast directly
          const triggerType = policy.trigger_type as TriggerType

          return (
            <div key={policy.id} className={`${styles.row} ${styles.policyRow}`}>
              <span className={styles.colPolicy}>
                <Link to={`/policies/${policy.id}`} className={styles.policyLink}>
                  {policy.name}
                </Link>
                <TriggerChip type={triggerType} />
              </span>

              <span className={styles.colStatus}>
                <RunCell policy={policy} />
              </span>

              <span className={styles.colWhen}>
                {run ? (
                  <span className={styles.muted}>{fmtRel(run.started_at)}</span>
                ) : (
                  <span className={styles.muted}>—</span>
                )}
              </span>

              {/* Duration is not available in ApiRunSummary; always show dash */}
              <span className={`${styles.colDuration} ${styles.muted}`}>
                {fmtDur(null)}
              </span>

              <span className={styles.colTokens}>
                {run ? (
                  <span className={styles.mono}>{fmtTok(run.token_cost)}</span>
                ) : (
                  <span className={styles.muted}>—</span>
                )}
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}
