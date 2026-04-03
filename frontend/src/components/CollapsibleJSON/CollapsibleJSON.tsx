import { useState } from 'react'
import { CopyBlock } from '@/components/CopyBlock'
import styles from './CollapsibleJSON.module.css'

const PREVIEW_LINES = 12

interface Props {
  value: unknown
  defaultCollapsed?: boolean
}

export function CollapsibleJSON({ value, defaultCollapsed = true }: Props) {
  const [collapsed, setCollapsed] = useState(defaultCollapsed)

  const text = JSON.stringify(value, null, 2)
  const lines = text.split('\n')
  const isLong = lines.length > PREVIEW_LINES

  const displayText = collapsed && isLong
    ? lines.slice(0, PREVIEW_LINES).join('\n')
    : text

  const hiddenCount = lines.length - PREVIEW_LINES

  return (
    <CopyBlock text={text}>
      <pre className={styles.pre}>
        <code>{displayText}</code>
        {collapsed && isLong && (
          <span className={styles.ellipsis}>
            {` … ${hiddenCount} more line${hiddenCount === 1 ? '' : 's'}`}
          </span>
        )}
      </pre>
      {isLong && (
        <button
          type="button"
          className={styles.toggleBtn}
          onClick={() => setCollapsed((c) => !c)}
        >
          {collapsed ? 'Show all' : 'Collapse'}
        </button>
      )}
    </CopyBlock>
  )
}
