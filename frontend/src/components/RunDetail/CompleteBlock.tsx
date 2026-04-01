import type { ParsedStep } from './types'
import { formatDuration } from '@/utils/format'
import styles from './CompleteBlock.module.css'

interface Props {
  step: ParsedStep & { type: 'complete' }
  // Duration is passed in from the parent, which computes it from the run's
  // started_at and completed_at via computeRunDuration. Null when not yet available.
  durationSeconds: number | null
}

export function CompleteBlock({ durationSeconds }: Props) {
  return (
    <div className={styles.block}>
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.label}>Run complete</span>
      {durationSeconds !== null && (
        <span className={styles.duration}>{formatDuration(durationSeconds)}</span>
      )}
    </div>
  )
}
