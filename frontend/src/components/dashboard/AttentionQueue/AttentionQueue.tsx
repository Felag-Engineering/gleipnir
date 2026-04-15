import { ChevronDown } from 'lucide-react'
import type { AttentionItem as AttentionItemType } from '@/hooks/useAttentionItems'
import { useLocalStorage } from '@/hooks/useLocalStorage'
import { AttentionItem } from './AttentionItem'
import styles from './AttentionQueue.module.css'

const COLLAPSED_STORAGE_KEY = 'gleipnir-attention-collapsed'

interface AttentionQueueProps {
  items: AttentionItemType[]
  count: number
  onDismiss: (runId: string) => void
}

export function AttentionQueue({ items, count, onDismiss }: AttentionQueueProps) {
  const [collapsed, setCollapsed] = useLocalStorage(COLLAPSED_STORAGE_KEY, false)

  if (count === 0) return null

  function toggleCollapsed() {
    setCollapsed(!collapsed)
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
