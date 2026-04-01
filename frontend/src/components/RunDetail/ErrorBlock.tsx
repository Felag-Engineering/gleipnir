import type { ParsedStep } from './types'
import styles from './ErrorBlock.module.css'

interface Props {
  step: ParsedStep & { type: 'error' }
}

export function ErrorBlock({ step }: Props) {
  return (
    <div className={styles.block}>
      <div className={styles.header}>
        <span className={styles.dot} aria-hidden="true" />
        <span className={styles.label}>Error</span>
        {step.content.code && (
          <span className={styles.code}>{step.content.code}</span>
        )}
      </div>
      <div className={styles.content}>{step.content.message}</div>
    </div>
  )
}
