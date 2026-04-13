import type { ParsedStep } from './types'
import { formatDurationMs } from '@/utils/format'
import styles from './CompleteBlock.module.css'

interface Props {
  step: ParsedStep & { type: 'complete' }
  // Duration in milliseconds, passed from StepTimeline which receives it from
  // useLiveDuration via RunDetailPage. Null when not yet available.
  durationMs: number | null
}

export function CompleteBlock({ durationMs }: Props) {
  return (
    <div className={styles.block}>
      <span className={styles.dot} aria-hidden="true" />
      <span className={styles.label}>Run complete</span>
      {durationMs !== null && (
        <span className={styles.duration}>{formatDurationMs(durationMs)}</span>
      )}
    </div>
  )
}
