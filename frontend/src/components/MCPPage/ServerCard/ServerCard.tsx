import type { ApiMcpServer, ApiMcpTool } from '@/api/types'
import { HealthIndicator } from '@/components/MCPPage/HealthIndicator'
import type { HealthStatus } from '@/components/MCPPage/HealthIndicator'
import { ToolList } from '@/components/MCPPage/ToolList'
import styles from './ServerCard.module.css'

function fmtAgo(iso: string): string {
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

interface Props {
  server: ApiMcpServer
  tools: ApiMcpTool[] | undefined
  toolsLoading: boolean
  isDiscovering: boolean
  onDiscover: (serverId: string) => void
  onDelete: (server: ApiMcpServer, toolCount: number) => void
  onRoleChange: (toolId: string, serverId: string, role: 'sensor' | 'actuator' | 'feedback') => void
  updatingToolId: string | null
}

export function ServerCard({
  server,
  tools,
  toolsLoading,
  isDiscovering,
  onDiscover,
  onDelete,
  onRoleChange,
  updatingToolId,
}: Props) {
  const health: HealthStatus = isDiscovering
    ? 'discovering'
    : server.last_discovered_at === null
    ? 'unreachable'
    : 'connected'

  const toolCount = tools?.length ?? 0

  return (
    <div className={styles.card}>
      <div className={styles.header}>
        <div className={styles.info}>
          <div className={styles.titleRow}>
            <HealthIndicator status={health} />
            <h2 className={styles.name}>{server.name}</h2>
          </div>
          <div className={styles.url}>{server.url}</div>
          {server.last_discovered_at && (
            <div className={styles.meta}>
              Discovered {fmtAgo(server.last_discovered_at)}
            </div>
          )}
        </div>
        <div className={styles.actions}>
          <button
            type="button"
            className={styles.discoverBtn}
            onClick={() => onDiscover(server.id)}
            disabled={isDiscovering}
            aria-label={`Discover tools for ${server.name}`}
          >
            {isDiscovering ? (
              <>
                <span className={styles.spinner} aria-hidden="true" />
                Discovering…
              </>
            ) : (
              <>
                <span className={styles.discoverIcon} aria-hidden="true">↻</span>
                Discover
              </>
            )}
          </button>
          <button
            type="button"
            className={styles.deleteBtn}
            onClick={() => onDelete(server, toolCount)}
            aria-label={`Delete ${server.name}`}
          >
            Delete
          </button>
        </div>
      </div>
      <ToolList
        tools={tools}
        isLoading={toolsLoading}
        onRoleChange={onRoleChange}
        updatingToolId={updatingToolId}
      />
    </div>
  )
}
