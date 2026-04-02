import { useMcpServers } from '@/hooks/useMcpServers'
import styles from './McpHealthDot.module.css'

/**
 * Orange pulsing dot that appears when any MCP server is unhealthy
 * (never successfully discovered). Renders nothing when all servers
 * are healthy or when no servers are configured.
 */
export function McpHealthDot() {
  const { data: servers } = useMcpServers()

  if (!servers || servers.length === 0) return null

  const hasUnhealthy = servers.some(s => s.last_discovered_at === null)
  if (!hasUnhealthy) return null

  const unhealthyCount = servers.filter(s => s.last_discovered_at === null).length

  return (
    <span
      className={styles.dot}
      title={`${unhealthyCount} MCP server${unhealthyCount > 1 ? 's' : ''} unreachable`}
      aria-label={`${unhealthyCount} MCP server${unhealthyCount > 1 ? 's' : ''} unreachable`}
    />
  )
}
