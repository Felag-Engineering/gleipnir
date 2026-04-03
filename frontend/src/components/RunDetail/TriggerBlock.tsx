import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import styles from './TriggerBlock.module.css'

interface Props {
  triggerType: string
  payload: string | null
}

export function TriggerBlock({ triggerType, payload }: Props) {
  let parsedPayload: unknown = null
  if (payload && payload !== '{}' && payload !== 'null') {
    try { parsedPayload = JSON.parse(payload) } catch { parsedPayload = payload }
  }

  return (
    <div className={styles.block}>
      <div className={styles.header}>
        <span className={styles.label}>Trigger</span>
        <span className={styles.typePill}>{triggerType}</span>
      </div>
      {parsedPayload !== null && (
        <div className={styles.body}>
          <CollapsibleJSON value={parsedPayload} />
        </div>
      )}
    </div>
  )
}
