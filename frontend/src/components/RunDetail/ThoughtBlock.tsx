import { useState } from 'react'
import { renderInlineMarkdown } from '@/utils/inlineMarkdown'
import type { ParsedStep } from './types'
import styles from './ThoughtBlock.module.css'

interface Props {
  step: ParsedStep & { type: 'thought' }
  // The run's final thought is usually the agent's answer, and an answer that
  // opens collapsed hides the one thing the reader came for.
  defaultExpanded?: boolean
}

const COLLAPSE_THRESHOLD = 200

// collapseText cuts text to at most COLLAPSE_THRESHOLD characters, preferring
// the last line break and then the last space inside the limit. Inline
// markdown never spans a line, so cutting at a line break cannot leave a
// dangling `**` that renders as literal asterisks; a space at least keeps
// whole words.
export function collapseText(text: string): string {
  const head = text.slice(0, COLLAPSE_THRESHOLD)
  const lastBreak = head.lastIndexOf('\n')
  if (lastBreak > COLLAPSE_THRESHOLD / 2) {
    return head.slice(0, lastBreak).trimEnd() + '...'
  }
  const lastSpace = head.lastIndexOf(' ')
  if (lastSpace > COLLAPSE_THRESHOLD / 2) {
    return head.slice(0, lastSpace) + '...'
  }
  return head + '...'
}

export function ThoughtBlock({ step, defaultExpanded = false }: Props) {
  const text = step.content.text
  const isLong = text.length > COLLAPSE_THRESHOLD
  const [expanded, setExpanded] = useState(defaultExpanded)

  const displayText = isLong && !expanded ? collapseText(text) : text

  return (
    <div className={styles.block}>
      <div className={styles.header}>
        <span className={styles.label}>Thought</span>
      </div>
      <div className={styles.content}>
        {renderInlineMarkdown(displayText)}
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
