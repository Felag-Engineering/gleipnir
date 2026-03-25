import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import type { ApiMcpTool } from '@/api/types'
import styles from './ToolRow.module.css'

const ROLES = ['tool', 'feedback'] as const
type Role = typeof ROLES[number]

interface Props {
  tool: ApiMcpTool
  onRoleChange: (toolId: string, role: Role) => void
  isUpdating: boolean
}

export function ToolRow({ tool, onRoleChange, isUpdating }: Props) {
  return (
    <div className={styles.row}>
      <div className={styles.main}>
        <div className={styles.nameRow}>
          <span className={styles.name}>{tool.name}</span>
          <select
            className={`${styles.roleSelect} ${styles[tool.capability_role]}`}
            value={tool.capability_role}
            onChange={(e) => {
              const role = e.target.value as Role
              if (ROLES.includes(role)) {
                onRoleChange(tool.id, role)
              }
            }}
            disabled={isUpdating}
            aria-label={`Role for ${tool.name}`}
          >
            {ROLES.map((r) => (
              <option key={r} value={r}>{r}</option>
            ))}
          </select>
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
