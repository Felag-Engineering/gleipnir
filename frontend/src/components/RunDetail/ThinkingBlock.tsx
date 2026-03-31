import { useState } from 'react'
import type { ParsedStep } from './types'
import { formatTokens } from '@/utils/format'
import styles from './ThinkingBlock.module.css'

interface Props {
  step: ParsedStep & { type: 'thinking' }
  defaultExpanded?: boolean
}

export function ThinkingBlock({ step, defaultExpanded = false }: Props) {
  const [expanded, setExpanded] = useState(defaultExpanded)

  const tokenCount = step.raw.token_cost
  const isRedacted = step.content.redacted

  function toggle() {
    setExpanded(v => !v)
  }

  function handleKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      setExpanded(v => !v)
    }
  }

  const blockClass = expanded
    ? `${styles.block} ${styles.blockExpanded}`
    : styles.block

  const chevronClass = expanded
    ? `${styles.chevron} ${styles.chevronExpanded}`
    : styles.chevron

  const contentClass = isRedacted
    ? `${styles.content} ${styles.redacted}`
    : styles.content

  return (
    <div
      className={blockClass}
      onClick={toggle}
      role="button"
      tabIndex={0}
      onKeyDown={handleKeyDown}
      aria-expanded={expanded}
    >
      <div className={styles.header}>
        <span className={chevronClass} aria-hidden="true">▶</span>
        <span className={styles.label}>Thinking</span>
        {tokenCount > 0 && (
          <span className={styles.tokens}>{formatTokens(tokenCount)} tokens</span>
        )}
      </div>
      {expanded && (
        <div className={contentClass}>
          {isRedacted ? '[redacted]' : step.content.text}
        </div>
      )}
    </div>
  )
}
