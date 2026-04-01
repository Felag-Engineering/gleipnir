import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import type { ApiMcpTool } from '@/api/types'
import styles from './ToolRow.module.css'

interface Props {
  tool: ApiMcpTool
}

export function ToolRow({ tool }: Props) {
  return (
    <div className={styles.row}>
      <div className={styles.main}>
        <div className={styles.nameRow}>
          <span className={styles.name}>{tool.name}</span>
        </div>
        {tool.description && (
          <div className={styles.description}>{tool.description}</div>
        )}
        <div className={styles.schema}>
          <CollapsibleJSON value={tool.input_schema} defaultCollapsed={true} />
        </div>
      </div>
    </div>
  )
}
