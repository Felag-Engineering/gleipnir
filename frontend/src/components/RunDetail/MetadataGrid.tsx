import type { ApiRun } from '@/api/types'
import styles from './MetadataGrid.module.css'

interface Props {
  run: ApiRun
  toolCallCount: number
  tokenTotal: number
  duration: number | null
}

function fmtDuration(ms: number): string {
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const rem = s % 60
  return `${m}m ${rem}s`
}

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

function fmtAbs(iso: string): string {
  try {
    return new Date(iso).toLocaleString()
  } catch {
    return iso
  }
}

interface GridCell {
  label: string
  value: string
}

export function MetadataGrid({ run, toolCallCount, tokenTotal, duration }: Props) {
  const cells: GridCell[] = [
    { label: 'Run ID', value: run.id },
    { label: 'Started', value: fmtAbs(run.started_at) },
    { label: 'Duration', value: duration !== null ? fmtDuration(duration) : '—' },
    { label: 'Tokens', value: fmtTokens(tokenTotal) },
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
