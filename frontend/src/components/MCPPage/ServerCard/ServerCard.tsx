import type { ApiMcpServer, ApiMcpTool } from '@/api/types'
import { ProtocolBadge } from '@/components/MCPPage/ProtocolBadge'
import { SkeletonBlock } from '@/components/SkeletonBlock'
import { formatTimeAgo } from '@/utils/format'
import styles from './ServerCard.module.css'

const TOOL_CHIP_LIMIT = 4

interface Props {
  server: ApiMcpServer
  tools: ApiMcpTool[] | undefined
  toolsLoading: boolean
  isDiscovering: boolean
  onClick: () => void
}

export function ServerCard({
  server,
  tools,
  toolsLoading,
  isDiscovering,
  onClick,
}: Props) {
  // A managed entry is a plugin instance's endpoint: same MCP client stack,
  // same tool namespace, but its URL and name belong to the plugin lifecycle.
  // Saying so on the card is what keeps an operator from reading an
  // unfamiliar server as one they forgot registering.
  const isManaged = server.trust_tier === 'managed'
  const isUnreachable = server.last_discovered_at === null
  const hasDrift = server.has_drift
  const toolCount = tools?.length ?? 0
  const previewTools = tools?.slice(0, TOOL_CHIP_LIMIT) ?? []
  const remaining = toolCount - previewTools.length

  return (
    <div
      className={`${styles.card} ${isUnreachable ? styles.cardUnreachable : ''}`}
      onClick={onClick}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onClick()
        }
      }}
      role="button"
      tabIndex={0}
      aria-label={`${server.name}, ${toolCount} tools`}
    >
      <div className={styles.topRow}>
        <div className={styles.titleGroup}>
          <span className={styles.name}>{server.name}</span>
          <span className={styles.toolCountBadge}>
            {toolCount} {toolCount === 1 ? 'tool' : 'tools'}
          </span>
          <ProtocolBadge version={server.protocol_version} />
          {isManaged && (
            <span className={styles.managedBadge} title="Managed by a plugin's lifecycle">
              Plugin
            </span>
          )}
          {isDiscovering && <span className={styles.discoveringBadge}>Discovering...</span>}
          {!isDiscovering && hasDrift && <span className={styles.driftBadge}>Drift</span>}
          {!isDiscovering && isUnreachable && (
            <span className={styles.unreachableBadge}>Unreachable</span>
          )}
        </div>
        {server.last_discovered_at && (
          <span className={styles.discoveredAt}>
            Discovered {formatTimeAgo(server.last_discovered_at)}
          </span>
        )}
      </div>
      <div className={styles.url}>{server.url}</div>
      <div className={styles.toolChips}>
        {isDiscovering || toolsLoading ? (
          <SkeletonBlock height={22} />
        ) : (
          <>
            {previewTools.map((tool) => (
              <span key={tool.id} className={styles.chip}>
                {tool.name}
              </span>
            ))}
            {remaining > 0 && (
              <span className={styles.chipMore}>+{remaining} more</span>
            )}
          </>
        )}
      </div>
    </div>
  )
}
