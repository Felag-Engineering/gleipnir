import { useEffect, useMemo, useState } from 'react'
import { Search } from 'lucide-react'
import type { ApiMcpServer, ApiMcpTool, ApiPolicyListItem } from '@/api/types'
import { ToolAccordionRow } from '@/components/MCPPage/ToolAccordionRow'
import { SkeletonBlock } from '@/components/SkeletonBlock'
import { formatTimeAgo } from '@/utils/format'
import styles from './ServerDetailModal.module.css'

interface Props {
  server: ApiMcpServer
  tools: ApiMcpTool[] | undefined
  toolsLoading: boolean
  isDiscovering: boolean
  policies: ApiPolicyListItem[] | undefined
  onClose: () => void
  onDiscover: (serverId: string) => void
  onDelete: (server: ApiMcpServer, toolCount: number) => void
}

/** Build a map of "server.tool" → list of policy names that reference it. */
function buildToolUsageMap(
  serverName: string,
  policies: ApiPolicyListItem[] | undefined,
): Map<string, string[]> {
  const map = new Map<string, string[]>()
  if (!policies) return map
  for (const policy of policies) {
    for (const ref of policy.tool_refs) {
      // tool_refs are "server.tool_name" — only include if server matches
      if (ref.startsWith(serverName + '.')) {
        const existing = map.get(ref) ?? []
        existing.push(policy.name)
        map.set(ref, existing)
      }
    }
  }
  return map
}

export function ServerDetailModal({
  server,
  tools,
  toolsLoading,
  isDiscovering,
  policies,
  onClose,
  onDiscover,
  onDelete,
}: Props) {
  const [expandedToolId, setExpandedToolId] = useState<string | null>(null)
  const [filter, setFilter] = useState('')
  const toolCount = tools?.length ?? 0
  const isUnreachable = server.last_discovered_at === null
  const hasDrift = server.has_drift

  const toolUsageMap = useMemo(
    () => buildToolUsageMap(server.name, policies),
    [server.name, policies],
  )

  const filteredTools = useMemo(() => {
    if (!tools) return undefined
    if (!filter) return tools
    const q = filter.toLowerCase()
    return tools.filter(
      (tool) =>
        tool.name.toLowerCase().includes(q) ||
        tool.description.toLowerCase().includes(q),
    )
  }, [tools, filter])

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [onClose])

  // Collapse expanded tool when it gets filtered out
  useEffect(() => {
    if (expandedToolId && filteredTools && !filteredTools.some((t) => t.id === expandedToolId)) {
      setExpandedToolId(null)
    }
  }, [filteredTools, expandedToolId])

  const showFilter = !toolsLoading && toolCount > 5

  return (
    <div
      className={styles.overlay}
      role="dialog"
      aria-modal="true"
      aria-label={`${server.name} details`}
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div className={styles.box}>
        <div className={styles.header}>
          <div className={styles.headerTop}>
            <div className={styles.titleGroup}>
              <h2 className={styles.serverName}>{server.name}</h2>
              <span className={styles.toolCountBadge}>
                {toolCount} {toolCount === 1 ? 'tool' : 'tools'}
              </span>
              {hasDrift && <span className={styles.driftBadge}>Drift</span>}
              {isUnreachable && <span className={styles.unreachableBadge}>Unreachable</span>}
            </div>
            <button
              type="button"
              className={styles.closeBtn}
              aria-label="Close"
              onClick={onClose}
            >
              &times;
            </button>
          </div>
          <div className={styles.headerBottom}>
            <div className={styles.meta}>
              <span>{server.url}</span>
              {server.last_discovered_at && (
                <>
                  <span className={styles.metaSep}>&middot;</span>
                  <span>Discovered {formatTimeAgo(server.last_discovered_at)}</span>
                </>
              )}
            </div>
            <div className={styles.actions}>
              <button
                type="button"
                className={styles.discoverBtn}
                onClick={() => onDiscover(server.id)}
                disabled={isDiscovering}
              >
                {isDiscovering ? (
                  <>
                    <span className={styles.spinner} aria-hidden="true" />
                    Discovering...
                  </>
                ) : (
                  <>&#x21bb; Rediscover</>
                )}
              </button>
              <button
                type="button"
                className={styles.deleteBtn}
                onClick={() => onDelete(server, toolCount)}
              >
                Delete
              </button>
            </div>
          </div>
        </div>

        {showFilter && (
          <div className={styles.filterBar}>
            <Search size={14} className={styles.filterIcon} />
            <input
              type="text"
              className={styles.filterInput}
              placeholder="Filter tools..."
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              aria-label="Filter tools"
            />
          </div>
        )}

        <div className={styles.body}>
          {toolsLoading ? (
            <div className={styles.loadingContainer}>
              <SkeletonBlock height={44} />
              <SkeletonBlock height={44} />
              <SkeletonBlock height={44} />
            </div>
          ) : filteredTools && filteredTools.length > 0 ? (
            filteredTools.map((tool) => {
              const ref = `${server.name}.${tool.name}`
              const usedBy = toolUsageMap.get(ref)
              return (
                <ToolAccordionRow
                  key={tool.id}
                  tool={tool}
                  expanded={expandedToolId === tool.id}
                  onToggle={() => setExpandedToolId(expandedToolId === tool.id ? null : tool.id)}
                  usedBy={usedBy}
                />
              )
            })
          ) : filter && tools && tools.length > 0 ? (
            <div className={styles.empty}>No tools matching "{filter}"</div>
          ) : (
            <div className={styles.empty}>
              No tools discovered. Click Rediscover to fetch tools from this server.
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
