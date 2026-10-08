import { useMemo } from 'react'
import type { ApiMcpTool } from '@/api/types'
import { CopyBlock } from '@/components/CopyBlock'
import { CollapsibleJSON } from '@/components/CollapsibleJSON'
import { ArgEnforcementBadge, explainArgEnforcement } from '@/components/MCPPage/ArgEnforcementBadge'
import { SimplifiedBadge } from '@/components/MCPPage/SimplifiedBadge'
import { parseSchema, type ParsedParam } from './schemaParams'
import styles from './ToolDetail.module.css'

interface Props {
  tool: ApiMcpTool
  serverName: string
  usedBy?: string[]
  // Enable/disable control — only rendered when canManage is true.
  canManage?: boolean
  onSetEnabled?: (enabled: boolean) => void
  isUpdatingEnabled?: boolean
}

// Every string a tool carries (name, description, parameter descriptions,
// enum values) is chosen by the MCP server, so this component renders them
// only as plain text nodes — never through a markdown or HTML renderer.
export function ToolDetail({
  tool,
  serverName,
  usedBy,
  canManage,
  onSetEnabled,
  isUpdatingEnabled,
}: Props) {
  const { params, combinator } = useMemo(() => parseSchema(tool.input_schema), [tool.input_schema])
  const topLevelCount = params.filter((p) => p.depth === 0).length
  const isDisabled = tool.enabled === false
  const enforcement = explainArgEnforcement(tool.arg_enforcement)
  // Only top-level parameters can carry an outbound header.
  const outboundHeaders = useMemo(
    () => new Map((tool.outbound_headers ?? []).map((h) => [h.parameter, h.header])),
    [tool.outbound_headers],
  )
  // The name an agent's policy grants: "<server>.<tool>".
  const reference = `${serverName}.${tool.name}`

  return (
    <article className={styles.detail} aria-label={`${tool.name} details`}>
      <header className={styles.header}>
        <div className={styles.titleRow}>
          <h3 className={styles.name}>{tool.name}</h3>
          <span className={isDisabled ? styles.statusDisabled : styles.statusEnabled}>
            {isDisabled ? 'Disabled' : 'Enabled'}
          </span>
          <SimplifiedBadge providers={tool.simplified_for ?? []} />
          <ArgEnforcementBadge state={tool.arg_enforcement} />
          {canManage && onSetEnabled && (
            <button
              type="button"
              className={styles.toggleEnabledBtn}
              onClick={() => onSetEnabled(isDisabled)}
              disabled={isUpdatingEnabled}
            >
              {isUpdatingEnabled
                ? isDisabled ? 'Enabling...' : 'Disabling...'
                : isDisabled ? 'Enable tool' : 'Disable tool'}
            </button>
          )}
        </div>
        {isDisabled && (
          <p className={styles.disabledNote}>
            Disabled tools are never registered with an agent, even when its policy grants them.
          </p>
        )}
        {enforcement && (
          <p className={styles.disabledNote}>
            Argument checking: reduced — {enforcement.reason}. {enforcement.detail}
          </p>
        )}
      </header>

      <section className={styles.section}>
        <h4 className={styles.sectionTitle}>Policy reference</h4>
        <CopyBlock text={reference}>
          <code className={styles.reference}>{reference}</code>
        </CopyBlock>
      </section>

      <section className={styles.section}>
        <h4 className={styles.sectionTitle}>Description</h4>
        {tool.description ? (
          <p className={styles.description}>{tool.description}</p>
        ) : (
          <p className={styles.muted}>The server did not provide a description.</p>
        )}
      </section>

      <section className={styles.section}>
        <h4 className={styles.sectionTitle}>Used by</h4>
        {usedBy && usedBy.length > 0 ? (
          <div className={styles.pills}>
            {usedBy.map((name) => (
              <span key={name} className={styles.agentPill}>{name}</span>
            ))}
          </div>
        ) : (
          <p className={styles.muted}>No agent grants this tool.</p>
        )}
      </section>

      <section className={styles.section}>
        <h4 className={styles.sectionTitle}>
          Parameters
          {/* Top-level only, matching the count in the tool list. */}
          {topLevelCount > 0 && <span className={styles.count}>{topLevelCount}</span>}
        </h4>
        {combinator && (
          <p className={styles.note}>
            This schema uses <code>{combinator}</code> at its root, so the list below may be
            incomplete. The input schema further down is the full definition.
          </p>
        )}
        {tool.outbound_headers_rejected && (
          <p className={styles.note}>
            This tool asks for a parameter to be sent as an outbound header under a name that is not
            allowed, so every call to it is rejected.
          </p>
        )}
        {params.length > 0 ? (
          <ul className={styles.paramList}>
            {params.map((p) => (
              <ParamRow
                key={p.path}
                param={p}
                outboundHeader={p.depth === 0 ? outboundHeaders.get(p.path) : undefined}
              />
            ))}
          </ul>
        ) : (
          !combinator && <p className={styles.muted}>This tool takes no parameters.</p>
        )}
      </section>

      <section className={styles.section}>
        <h4 className={styles.sectionTitle}>Input schema</h4>
        <CollapsibleJSON value={tool.input_schema} />
      </section>
    </article>
  )
}

function ParamRow({ param, outboundHeader }: { param: ParsedParam; outboundHeader?: string }) {
  const depthClass = param.depth === 1 ? styles.depth1 : param.depth >= 2 ? styles.depth2 : ''
  return (
    <li className={`${styles.param} ${depthClass}`}>
      <div className={styles.paramHead}>
        <code className={styles.paramName}>{param.path}</code>
        <span className={styles.paramType}>{param.type}</span>
        {param.required ? (
          <span className={styles.required}>required</span>
        ) : (
          <span className={styles.optional}>optional</span>
        )}
      </div>
      {param.description && <p className={styles.paramDescription}>{param.description}</p>}
      {outboundHeader && (
        <p className={styles.outboundHeader}>
          Sends an outbound header: <code className={styles.value}>{outboundHeader}</code>
        </p>
      )}
      {(param.allowedValues || param.defaultValue !== undefined || param.constraints.length > 0) && (
        <dl className={styles.facts}>
          {param.allowedValues && (
            <div className={styles.fact}>
              <dt>Allowed</dt>
              <dd className={styles.valueList}>
                {param.allowedValues.map((v) => (
                  <code key={v} className={styles.value}>{v}</code>
                ))}
              </dd>
            </div>
          )}
          {param.defaultValue !== undefined && (
            <div className={styles.fact}>
              <dt>Default</dt>
              <dd><code className={styles.value}>{param.defaultValue}</code></dd>
            </div>
          )}
          {param.constraints.length > 0 && (
            <div className={styles.fact}>
              <dt>Limits</dt>
              <dd>{param.constraints.join(' · ')}</dd>
            </div>
          )}
        </dl>
      )}
    </li>
  )
}
