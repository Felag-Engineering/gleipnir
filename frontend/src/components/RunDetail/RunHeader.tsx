import { useNavigate } from 'react-router-dom'
import type { ApiRun } from '@/api/types'
import { StatusBadge } from '@/components/dashboard/StatusBadge/StatusBadge'
import { TriggerChip } from '@/components/dashboard/TriggerChip/TriggerChip'
import type { RunStatus, TriggerType } from '@/constants/status'
import { formatDurationMs, formatTokens, formatTimestamp } from '@/utils/format'
import styles from './RunHeader.module.css'

interface Props {
  run: ApiRun
  toolCallCount: number
  tokenTotal: number
  duration: number | null
}

interface MetadataCell {
  label: string
  value: string
}

export function RunHeader({ run, toolCallCount, tokenTotal, duration }: Props) {
  const navigate = useNavigate()

  const cells: MetadataCell[] = [
    { label: 'Run ID', value: run.id },
    { label: 'Started', value: formatTimestamp(run.started_at) },
    { label: 'Duration', value: duration !== null ? formatDurationMs(duration) : '—' },
    { label: 'Tokens', value: formatTokens(tokenTotal) },
    { label: 'Tool Calls', value: String(toolCallCount) },
  ]

  return (
    <header className={styles.header}>
      <div className={styles.row1}>
        <button
          type="button"
          className={styles.backBtn}
          onClick={() => navigate('/dashboard')}
        >
          ← Runs
        </button>
        <span className={styles.policyName}>
          {run.policy_name || run.policy_id}
        </span>
        <StatusBadge status={run.status as RunStatus} />
        <TriggerChip type={run.trigger_type as TriggerType} />
      </div>

      <dl className={styles.metadataRow}>
        {cells.map(({ label, value }) => (
          <div key={label} className={styles.cell}>
            <dt className={styles.label}>{label}</dt>
            <dd className={styles.value}>{value}</dd>
          </div>
        ))}
      </dl>
    </header>
  )
}
