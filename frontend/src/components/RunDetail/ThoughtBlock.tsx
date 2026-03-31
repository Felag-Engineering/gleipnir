import type { ParsedStep } from './types'
import styles from './ThoughtBlock.module.css'

interface Props {
  step: ParsedStep & { type: 'thought' }
}

export function ThoughtBlock({ step }: Props) {
  return (
    <div className={styles.block}>
      <div className={styles.header}>
        <span className={styles.dot} aria-hidden="true" />
        <span className={styles.label}>Thought</span>
      </div>
      <div className={styles.content}>{step.content.text}</div>
    </div>
  )
}
