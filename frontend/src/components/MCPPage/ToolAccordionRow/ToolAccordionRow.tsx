import type { ApiMcpTool } from '@/api/types'
import { ParamChip } from '@/components/MCPPage/ParamChip'
import styles from './ToolAccordionRow.module.css'

interface Props {
  tool: ApiMcpTool
  expanded: boolean
  onToggle: () => void
  usedBy?: string[]
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

export function ToolAccordionRow({ tool, expanded, onToggle, usedBy }: Props) {
  const params = parseParams(tool.input_schema)

  return (
    <div className={styles.row}>
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
        </div>
      )}
    </div>
  )
}
