import { useState } from 'react'
import { ChevronDown } from 'lucide-react'
import type { AttentionItem as AttentionItemType } from '@/hooks/useAttentionItems'
import { AttentionItem } from './AttentionItem'
import styles from './AttentionQueue.module.css'

const COLLAPSED_STORAGE_KEY = 'gleipnir-attention-collapsed'

interface AttentionQueueProps {
  items: AttentionItemType[]
  count: number
  onDismiss: (runId: string) => void
}

export function AttentionQueue({ items, count, onDismiss }: AttentionQueueProps) {
  const [collapsed, setCollapsed] = useState<boolean>(() => {
    try {
      return localStorage.getItem(COLLAPSED_STORAGE_KEY) === 'true'
    } catch {
      return false
    }
  })

  if (count === 0) return null

  function toggleCollapsed() {
    const next = !collapsed
    setCollapsed(next)
    try {
      localStorage.setItem(COLLAPSED_STORAGE_KEY, String(next))
    } catch {
      // localStorage unavailable; ignore
    }
  }

  return (
    <section className={styles.section}>
      <button className={styles.sectionHeader} onClick={toggleCollapsed}>
        <span className={styles.sectionTitle}>NEEDS ATTENTION</span>
        <span className={styles.countBadge}>{count}</span>
        <span className={`${styles.chevron} ${collapsed ? styles.chevronCollapsed : ''}`}>
          <ChevronDown size={14} aria-hidden strokeWidth={2} />
        </span>
      </button>

      {!collapsed && (
        <div className={styles.itemList}>
          {items.map(item => (
            <AttentionItem key={item.request_id || item.run_id} item={item} onDismiss={onDismiss} />
          ))}
        </div>
      )}
    </section>
  )
}
