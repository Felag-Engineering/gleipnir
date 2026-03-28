import { Link } from 'react-router-dom'
import type { ApiPolicyListItem } from '@/api/types'
import { StatusBadge } from '@/components/dashboard/StatusBadge'
import { TriggerChip } from '@/components/dashboard/TriggerChip'
import { KNOWN_TRIGGERS, isRunStatus } from '@/constants/status'
import type { RunStatus, TriggerType } from '@/constants/status'
import styles from './PolicyList.module.css'

interface Props {
  policies: ApiPolicyListItem[]
  onTrigger?: (policyId: string, policyName: string) => void
  linkTo?: 'runs' | 'editor'
  groupByFolder?: boolean
  renderRunCell?: (policy: ApiPolicyListItem) => React.ReactNode
}

// Group policies by their folder field, using "Default" for unset entries.
function groupPoliciesByFolder(policies: ApiPolicyListItem[]): Map<string, ApiPolicyListItem[]> {
  const groups = new Map<string, ApiPolicyListItem[]>()
  for (const policy of policies) {
    const folder = policy.folder || 'Default'
    const existing = groups.get(folder)
    if (existing) {
      existing.push(policy)
    } else {
      groups.set(folder, [policy])
    }
  }
  return groups
}

function DefaultRunCell({ policy }: { policy: ApiPolicyListItem }) {
  if (policy.latest_run && isRunStatus(policy.latest_run.status)) {
    return <StatusBadge status={policy.latest_run.status as RunStatus} />
  }
  return <span className={styles.noRun}>—</span>
}

export function PolicyList({
  policies,
  onTrigger,
  linkTo = 'runs',
  groupByFolder = true,
  renderRunCell,
}: Props) {
  const headerClass = onTrigger ? styles.headerRow : styles.headerRowNoActions

  function renderRow(policy: ApiPolicyListItem) {
    const href = linkTo === 'editor' ? `/policies/${policy.id}` : `/policies/${policy.id}/runs`
    const runCell = renderRunCell
      ? renderRunCell(policy)
      : <DefaultRunCell policy={policy} />

    if (onTrigger) {
      return (
        <div key={policy.id} className={styles.row}>
          <Link to={href} className={styles.rowLink}>
            <span className={styles.policyName}>{policy.name}</span>
            <span>
              {KNOWN_TRIGGERS.has(policy.trigger_type) && (
                <TriggerChip
                  type={policy.trigger_type as TriggerType}
                  pausedAt={policy.paused_at}
                />
              )}
            </span>
            <span>{runCell}</span>
          </Link>
          <span className={styles.rowAction}>
            <button
              className={styles.playBtn}
              onClick={() => onTrigger(policy.id, policy.name)}
              title="Run now"
              aria-label={`Run ${policy.name}`}
            >
              ▶
            </button>
          </span>
        </div>
      )
    }

    return (
      <div key={policy.id} className={styles.rowNoActions}>
        <Link to={href} className={styles.rowLinkFull}>
          <span className={styles.policyName}>{policy.name}</span>
          <span>
            {KNOWN_TRIGGERS.has(policy.trigger_type) && (
              <TriggerChip
                type={policy.trigger_type as TriggerType}
                pausedAt={policy.paused_at}
              />
            )}
          </span>
          <span>{runCell}</span>
        </Link>
      </div>
    )
  }

  function renderTable(items: ApiPolicyListItem[]) {
    return (
      <div className={styles.table}>
        <div className={headerClass}>
          <span>Policy</span>
          <span>Trigger</span>
          <span>Latest run</span>
          {onTrigger && <span />}
        </div>
        {items.map(renderRow)}
      </div>
    )
  }

  if (!groupByFolder) {
    return <div className={styles.root}>{renderTable(policies)}</div>
  }

  const groups = groupPoliciesByFolder(policies)

  return (
    <div className={styles.root}>
      {Array.from(groups.entries()).map(([folder, items]) => (
        <section key={folder} className={styles.group}>
          <h2 className={styles.folderName}>{folder}</h2>
          {renderTable(items)}
        </section>
      ))}
    </div>
  )
}
