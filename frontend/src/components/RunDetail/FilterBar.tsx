import styles from './FilterBar.module.css'

export type FilterKey = 'all' | 'tool' | 'thought' | 'thinking' | 'error' | 'approval'

interface FilterChip {
  key: FilterKey
  label: string
}

const CHIPS: FilterChip[] = [
  { key: 'all', label: 'All' },
  { key: 'tool', label: 'Tools' },
  { key: 'thought', label: 'Thoughts' },
  { key: 'thinking', label: 'Thinking' },
  { key: 'error', label: 'Errors' },
  { key: 'approval', label: 'Approvals' },
]

interface Props {
  active: FilterKey
  counts: Record<FilterKey, number>
  onChange: (key: FilterKey) => void
}

export function FilterBar({ active, counts, onChange }: Props) {
  return (
    <nav className={styles.bar} aria-label="Step filter">
      {CHIPS.map(({ key, label }) => (
        <button
          key={key}
          type="button"
          className={`${styles.chip} ${active === key ? styles.active : counts[key] === 0 && key !== 'all' ? styles.chipEmpty : ''}`}
          onClick={() => onChange(key)}
        >
          {label}
          {counts[key] > 0 && (
            <span className={styles.badge}>{counts[key]}</span>
          )}
        </button>
      ))}
    </nav>
  )
}
