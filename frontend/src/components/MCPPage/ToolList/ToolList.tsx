import { useState } from 'react'
import { SkeletonBlock } from '@/components/SkeletonBlock'
import { ToolRow } from '@/components/MCPPage/ToolRow'
import type { ApiMcpTool } from '@/api/types'
import styles from './ToolList.module.css'

interface Props {
  tools: ApiMcpTool[] | undefined
  isLoading: boolean
  onRoleChange: (toolId: string, serverId: string, role: 'tool' | 'feedback') => void
  updatingToolId: string | null
}

export function ToolList({ tools, isLoading, onRoleChange, updatingToolId }: Props) {
  const [expanded, setExpanded] = useState(false)

  const toolCount = tools?.length ?? 0
  const label = `${toolCount} ${toolCount === 1 ? 'tool' : 'tools'}`

  return (
    <div className={styles.root}>
      <button
        type="button"
        className={styles.toggle}
        onClick={() => setExpanded((e) => !e)}
        aria-expanded={expanded}
      >
        <span className={`${styles.chevron} ${expanded ? styles.chevronOpen : ''}`} aria-hidden="true">▶</span>
        <span className={styles.toggleLabel}>{label}</span>
      </button>

      {expanded && (
        <div className={styles.list}>
          {isLoading ? (
            <>
              <SkeletonBlock height={56} />
              <SkeletonBlock height={56} />
              <SkeletonBlock height={56} />
            </>
          ) : tools && tools.length > 0 ? (
            tools.map((tool) => (
              <ToolRow
                key={tool.id}
                tool={tool}
                onRoleChange={(toolId, role) => onRoleChange(toolId, tool.server_id, role)}
                isUpdating={updatingToolId === tool.id}
              />
            ))
          ) : (
            <div className={styles.empty}>No tools discovered yet. Click Discover to fetch tools from this server.</div>
          )}
        </div>
      )}
    </div>
  )
}
