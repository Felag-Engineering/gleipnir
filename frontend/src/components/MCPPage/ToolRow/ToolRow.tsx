import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import type { ApiMcpTool } from '@/api/types'
import { CAPABILITY_ROLES, type CapabilityRole } from '@/constants/status'
import styles from './ToolRow.module.css'

interface Props {
  tool: ApiMcpTool
  onRoleChange: (toolId: string, role: CapabilityRole) => void
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
              const role = e.target.value as CapabilityRole
              if (CAPABILITY_ROLES.includes(role)) {
                onRoleChange(tool.id, role)
              }
            }}
            disabled={isUpdating}
            aria-label={`Role for ${tool.name}`}
          >
            {CAPABILITY_ROLES.map((r) => (
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
