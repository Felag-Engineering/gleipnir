import type { ApiMcpTool } from '@/api/types'
import { ParamChip } from '@/components/MCPPage/ParamChip'
import styles from './ToolAccordionRow.module.css'

interface Props {
  tool: ApiMcpTool
  expanded: boolean
  onToggle: () => void
  usedBy?: string[]
  // Enable/disable controls — only rendered when canManage is true.
  canManage?: boolean
  onSetEnabled?: (enabled: boolean) => void
  isUpdatingEnabled?: boolean
}

interface ParsedParam {
  name: string
  type: string
  required: boolean
}

function parseParams(schema: Record<string, unknown>): ParsedParam[] {
  const properties = schema.properties as Record<string, { type?: string }> | undefined
  if (!properties || typeof properties !== 'object') return []
  const requiredList = Array.isArray(schema.required) ? (schema.required as string[]) : []
  return Object.entries(properties).map(([name, prop]) => ({
    name,
    type: typeof prop?.type === 'string' ? prop.type : 'unknown',
    required: requiredList.includes(name),
  }))
}

function paramCountLabel(count: number): string {
  if (count === 0) return 'no params'
  return `${count} param${count === 1 ? '' : 's'}`
}

export function ToolAccordionRow({
  tool,
  expanded,
  onToggle,
  usedBy,
  canManage,
  onSetEnabled,
  isUpdatingEnabled,
}: Props) {
  const params = parseParams(tool.input_schema)
  const isDisabled = tool.enabled === false

  return (
    <div className={`${styles.row} ${isDisabled ? styles.rowDisabled : ''}`}>
      <button
        type="button"
        className={`${styles.toggle} ${expanded ? styles.toggleExpanded : ''}`}
        onClick={onToggle}
        aria-expanded={expanded}
      >
        <span
          className={`${styles.chevron} ${expanded ? styles.chevronOpen : ''}`}
          aria-hidden="true"
        >
          &#9654;
        </span>
        <span className={styles.toolName}>{tool.name}</span>
        {isDisabled && <span className={styles.disabledBadge}>Disabled</span>}
        <span className={styles.paramHint}>{paramCountLabel(params.length)}</span>
      </button>

      {expanded && (
        <div className={styles.detail}>
          {tool.description && (
            <div className={styles.description}>{tool.description}</div>
          )}
          {params.length > 0 ? (
            <>
              <div className={styles.paramLabel}>Parameters</div>
              <div className={styles.paramList}>
                {params.map((p) => (
                  <ParamChip key={p.name} name={p.name} type={p.type} required={p.required} />
                ))}
              </div>
            </>
          ) : (
            <div className={styles.noParams}>No parameters</div>
          )}
          {usedBy && usedBy.length > 0 && (
            <div className={styles.usedBy}>
              <span className={styles.usedByLabel}>Used by:</span>
              {usedBy.map((name) => (
                <span key={name} className={styles.agentPill}>{name}</span>
              ))}
            </div>
          )}
          {canManage && onSetEnabled && (
            <div className={styles.toggleEnabledRow}>
              <button
                type="button"
                className={styles.toggleEnabledBtn}
                onClick={() => onSetEnabled(!tool.enabled)}
                disabled={isUpdatingEnabled}
              >
                {isUpdatingEnabled
                  ? isDisabled ? 'Enabling...' : 'Disabling...'
                  : isDisabled ? 'Enable tool' : 'Disable tool'}
              </button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
