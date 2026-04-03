import type { ApiPolicyListItem } from '@/api/types'
import { PolicyCard } from './PolicyCard'
import styles from './PolicyList.module.css'

interface Props {
  policies: ApiPolicyListItem[]
  onTrigger?: (policyId: string, policyName: string) => void
  groupByFolder?: boolean
}

function groupPoliciesByFolder(policies: ApiPolicyListItem[]): Map<string, ApiPolicyListItem[]> {
  const groups = new Map<string, ApiPolicyListItem[]>()
  for (const policy of policies) {
    const folder = policy.folder || 'Ungrouped'
    const existing = groups.get(folder)
    if (existing) {
      existing.push(policy)
    } else {
      groups.set(folder, [policy])
    }
  }
  return groups
}

export function PolicyList({ policies, onTrigger, groupByFolder = true }: Props) {
  function renderCards(items: ApiPolicyListItem[]) {
    return (
      <div className={styles.cardList}>
        {items.map((policy) => (
          <PolicyCard
            key={policy.id}
            policy={policy}
            onTrigger={onTrigger ?? (() => {})}
          />
        ))}
      </div>
    )
  }

  if (!groupByFolder) {
    return <div className={styles.root}>{renderCards(policies)}</div>
  }

  const groups = groupPoliciesByFolder(policies)
  const showFolderHeaders = groups.size > 1

  return (
    <div className={styles.root}>
      {Array.from(groups.entries()).map(([folder, items]) => (
        <section key={folder} className={styles.group}>
          {showFolderHeaders && <h2 className={styles.folderName}>{folder}</h2>}
          {renderCards(items)}
        </section>
      ))}
    </div>
  )
}
