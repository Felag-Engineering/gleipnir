import type { ApiRun } from '@/api/types'
import { formatDurationMs, formatTokens, formatTimestamp } from '@/utils/format'
import styles from './MetadataGrid.module.css'

interface Props {
  run: ApiRun
  toolCallCount: number
  tokenTotal: number
  duration: number | null
}

interface GridCell {
  label: string
  value: string
}

export function MetadataGrid({ run, toolCallCount, tokenTotal, duration }: Props) {
  const cells: GridCell[] = [
    { label: 'Run ID', value: run.id },
    { label: 'Started', value: formatTimestamp(run.started_at) },
    { label: 'Duration', value: duration !== null ? formatDurationMs(duration) : '—' },
    { label: 'Tokens', value: formatTokens(tokenTotal) },
    { label: 'Tool calls', value: String(toolCallCount) },
  ]

  return (
    <dl className={styles.grid}>
      {cells.map(({ label, value }) => (
        <div key={label} className={styles.cell}>
          <dt className={styles.label}>{label}</dt>
          <dd className={styles.value}>{value}</dd>
        </div>
      ))}
    </dl>
  )
}
