import { useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import type { CapabilitySnapshotContent, CapabilitySnapshotV2, GrantedToolEntry } from './types'
import { isFeedbackEntry } from './types'
import { formatProviderName } from '@/utils/format'
import styles from './CapabilitySnapshotCard.module.css'

// Anchor the run header's capability summary scrolls to.
export const CAPABILITY_SNAPSHOT_ANCHOR = 'capability-snapshot'

interface Props {
  content: CapabilitySnapshotContent
  systemPrompt?: string | null
}

interface ServerGroup {
  server: string
  tools: GrantedToolEntry[]
}

// groupByServer keeps servers, and tools within a server, in snapshot order —
// the order the runtime registered them.
function groupByServer(tools: GrantedToolEntry[]): ServerGroup[] {
  const groups: ServerGroup[] = []
  const byServer = new Map<string, ServerGroup>()
  for (const tool of tools) {
    let group = byServer.get(tool.server_name)
    if (!group) {
      group = { server: tool.server_name, tools: [] }
      byServer.set(tool.server_name, group)
      groups.push(group)
    }
    group.tools.push(tool)
  }
  return groups
}

// OutboundHeaderNote shows which of a tool's parameters are sent as outbound
// headers. Renders nothing for a tool with none, and for snapshots written
// before the field existed.
function OutboundHeaderNote({ tool }: { tool: GrantedToolEntry }) {
  const headers = tool.outbound_headers ?? []
  if (headers.length === 0 && !tool.outbound_headers_rejected) return null
  return (
    <p className={styles.outboundNote}>
      <span className={styles.outboundTool}>{tool.tool_name}</span>
      {headers.length > 0 && (
        <>
          {' '}sends outbound headers:{' '}
          {headers.map((h, i) => (
            <span key={h.parameter}>
              {i > 0 && ', '}
              <code className={styles.outboundCode}>{h.header}</code> (from {h.parameter})
            </span>
          ))}
        </>
      )}
      {tool.outbound_headers_rejected && (
        <> declares an outbound header that was rejected, so none are sent.</>
      )}
    </p>
  )
}

// CapabilitySnapshotCard is the first entry of the run timeline: the exact
// tools registered with the agent at run start (ADR-018). The tool names are
// always visible, grouped by server, because the point of the card is what is
// *not* on it — a tool missing here did not exist for the agent (ADR-001).
export function CapabilitySnapshotCard({ content, systemPrompt }: Props) {
  const [promptExpanded, setPromptExpanded] = useState(false)

  // Support both the legacy array shape (pre-ADR-023) and the V2 object shape.
  const isV2 = !Array.isArray(content) && content !== null && typeof content === 'object'
  const tools = isV2 ? (content as CapabilitySnapshotV2).tools : (content as GrantedToolEntry[])
  const realTools = (tools ?? []).filter(t => !isFeedbackEntry(t))
  const feedbackEnabled = (tools ?? []).some(isFeedbackEntry)
  const modelName = isV2 ? (content as CapabilitySnapshotV2).model : undefined
  const provider = isV2 ? (content as CapabilitySnapshotV2).provider : undefined
  const count = realTools.length
  const groups = groupByServer(realTools)

  const meta = [
    `${count} tool${count === 1 ? '' : 's'}`,
    provider ? formatProviderName(provider) : undefined,
    modelName,
  ].filter(Boolean).join(' · ')

  return (
    <section
      id={CAPABILITY_SNAPSHOT_ANCHOR}
      className={styles.card}
      aria-label="Capability snapshot"
    >
      <div className={styles.header}>
        <span className={styles.label}>Capability snapshot</span>
        <span className={styles.meta}>{meta}</span>
        {feedbackEnabled && (
          <span className={styles.feedbackChip} title="gleipnir.ask_operator — human-in-the-loop channel">
            Feedback
          </span>
        )}
      </div>

      {groups.length === 0 ? (
        <p className={styles.none}>No tools were registered for this run.</p>
      ) : (
        <dl className={styles.groups}>
          {groups.map(group => (
            <div key={group.server} className={styles.group}>
              <dt className={styles.server}>{group.server}</dt>
              <dd className={styles.tools}>
                <ul className={styles.toolList}>
                  {group.tools.map(tool => (
                    <li key={tool.tool_name} className={styles.tool}>
                      {tool.tool_name}
                      {tool.approval === 'required' && (
                        <span className={styles.approvalTag}>approval</span>
                      )}
                    </li>
                  ))}
                </ul>
                {group.tools.map(tool => (
                  <OutboundHeaderNote key={tool.tool_name} tool={tool} />
                ))}
              </dd>
            </div>
          ))}
        </dl>
      )}

      {systemPrompt && (
        <>
          <button
            type="button"
            className={styles.promptToggle}
            onClick={() => setPromptExpanded((e) => !e)}
            aria-expanded={promptExpanded}
          >
            {promptExpanded ? <ChevronDown size={12} aria-hidden /> : <ChevronRight size={12} aria-hidden />} System prompt
          </button>
          {promptExpanded && (
            <pre className={styles.promptBody}>{systemPrompt}</pre>
          )}
        </>
      )}
    </section>
  )
}
