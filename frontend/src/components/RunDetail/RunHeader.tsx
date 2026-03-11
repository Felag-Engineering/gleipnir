import { useNavigate } from 'react-router-dom'
import type { ApiRun } from '@/api/types'
import { StatusBadge } from '@/components/dashboard/StatusBadge/StatusBadge'
import { TriggerChip } from '@/components/dashboard/TriggerChip/TriggerChip'
import type { RunStatus, TriggerType } from '@/components/dashboard/types'
import styles from './RunHeader.module.css'

interface Props {
  run: ApiRun
}

export function RunHeader({ run }: Props) {
  const navigate = useNavigate()

  return (
    <header className={styles.header}>
      <button
        type="button"
        className={styles.backBtn}
        onClick={() => navigate('/dashboard')}
      >
        ← Back
      </button>
      <div className={styles.meta}>
        <span className={styles.policyName}>
          {run.policy_name || run.policy_id}
        </span>
        <TriggerChip type={run.trigger_type as TriggerType} />
        <StatusBadge status={run.status as RunStatus} />
      </div>
    </header>
  )
}
