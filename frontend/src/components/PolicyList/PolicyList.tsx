import { Link } from 'react-router-dom'
import type { ApiPolicyListItem } from '../../api/types'
import { StatusBadge } from '../dashboard/StatusBadge'
import { TriggerChip } from '../dashboard/TriggerChip'
import type { RunStatus, TriggerType } from '../dashboard/types'
import styles from './PolicyList.module.css'

interface Props {
  policies: ApiPolicyListItem[]
}

// Group policies by their folder field, using "Default" for unset entries.
function groupByFolder(policies: ApiPolicyListItem[]): Map<string, ApiPolicyListItem[]> {
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

// RunStatus and TriggerType are strict unions; the API returns open strings.
// Only pass to the badge/chip when they match known values.
const KNOWN_STATUSES = new Set<string>([
  'complete', 'running', 'waiting_for_approval', 'failed', 'interrupted',
])
const KNOWN_TRIGGERS = new Set<string>(['webhook', 'cron', 'poll'])

export function PolicyList({ policies }: Props) {
  const groups = groupByFolder(policies)

  return (
    <div className={styles.root}>
      {Array.from(groups.entries()).map(([folder, items]) => (
        <section key={folder} className={styles.group}>
          <h2 className={styles.folderName}>{folder}</h2>
          <div className={styles.table}>
            <div className={styles.headerRow}>
              <span>Policy</span>
              <span>Trigger</span>
              <span>Latest run</span>
            </div>
            {items.map(policy => (
              <Link
                key={policy.id}
                to={`/policies/${policy.id}/runs`}
                className={styles.row}
              >
                <span className={styles.policyName}>{policy.name}</span>
                <span>
                  {KNOWN_TRIGGERS.has(policy.trigger_type) && (
                    <TriggerChip type={policy.trigger_type as TriggerType} />
                  )}
                </span>
                <span>
                  {policy.latest_run && KNOWN_STATUSES.has(policy.latest_run.status) ? (
                    <StatusBadge status={policy.latest_run.status as RunStatus} />
                  ) : (
                    <span className={styles.noRun}>—</span>
                  )}
                </span>
              </Link>
            ))}
          </div>
        </section>
      ))}
    </div>
  )
}
