import { useState } from 'react'
import type { ParsedStep } from './types'
import styles from './ThoughtBlock.module.css'

interface Props {
  step: ParsedStep & { type: 'thought' }
}

const COLLAPSE_THRESHOLD = 200

export function ThoughtBlock({ step }: Props) {
  const text = step.content.text
  const isLong = text.length > COLLAPSE_THRESHOLD
  const [expanded, setExpanded] = useState(false)

  const displayText = isLong && !expanded
    ? text.slice(0, COLLAPSE_THRESHOLD) + '...'
    : text

  return (
    <div className={styles.block}>
      <div className={styles.header}>
        <span className={styles.label}>Thought</span>
      </div>
      <div className={styles.content}>
        {displayText}
        {isLong && (
          <button
            type="button"
            className={styles.toggle}
            onClick={() => setExpanded(e => !e)}
          >
            {expanded ? 'Show less' : 'Show more'}
          </button>
        )}
      </div>
    </div>
  )
}
