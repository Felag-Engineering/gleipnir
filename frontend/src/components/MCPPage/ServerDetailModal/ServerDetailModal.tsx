import { useEffect, useMemo, useState } from 'react'
import FocusTrap from 'focus-trap-react'
import { Search } from 'lucide-react'
import type { ApiMcpServer, ApiMcpTool, ApiPolicyListItem } from '@/api/types'
import { MASKED_HEADER_VALUE } from '@/api/types'
import { ToolAccordionRow } from '@/components/MCPPage/ToolAccordionRow'
import { SkeletonBlock } from '@/components/SkeletonBlock'
import { formatTimeAgo } from '@/utils/format'
import { useUpdateMcpServer } from '@/hooks/mutations/servers'
import styles from './ServerDetailModal.module.css'

interface HeaderRow {
  key: string
  value: string
}

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

/** Build a map of "server.tool" → list of agent names that reference it. */
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
  const [showHeaderEditor, setShowHeaderEditor] = useState(false)
  const [headerRows, setHeaderRows] = useState<HeaderRow[]>([])
  const updateMutation = useUpdateMcpServer()

  const toolCount = tools?.length ?? 0
  const isUnreachable = server.last_discovered_at === null
  const hasDrift = server.has_drift

  // Seed the header editor from server.auth_header_keys when it opens.
  // Existing values are pre-filled with the masked sentinel so the backend
  // knows to preserve them unless the operator types a replacement.
  function openHeaderEditor() {
    const keys = server.auth_header_keys ?? []
    setHeaderRows(keys.map((k) => ({ key: k, value: MASKED_HEADER_VALUE })))
    setShowHeaderEditor(true)
  }

  function addHeaderRow() {
    setHeaderRows((prev) => [...prev, { key: '', value: '' }])
  }

  function removeHeaderRow(index: number) {
    setHeaderRows((prev) => prev.filter((_, i) => i !== index))
  }

  function updateKey(index: number, key: string) {
    setHeaderRows((prev) => prev.map((h, i) => (i === index ? { ...h, key } : h)))
  }

  function updateValue(index: number, value: string) {
    setHeaderRows((prev) => prev.map((h, i) => (i === index ? { ...h, value } : h)))
  }

  function handleSaveHeaders() {
    const nonEmpty = headerRows.filter((h) => h.key.trim())
    updateMutation.mutate(
      {
        id: server.id,
        name: server.name,
        url: server.url,
        auth_headers: nonEmpty,
      },
      {
        onSuccess: () => {
          setShowHeaderEditor(false)
          updateMutation.reset()
        },
      },
    )
  }

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
  const existingKeyCount = server.auth_header_keys?.length ?? 0

  return (
    <FocusTrap focusTrapOptions={{ initialFocus: false, allowOutsideClick: true, returnFocusOnDeactivate: true, fallbackFocus: '[role="dialog"]', escapeDeactivates: false }}>
      <div
        className={styles.overlay}
        onClick={(e) => {
          if (e.target === e.currentTarget) onClose()
        }}
      >
        <div
          className={styles.box}
          role="dialog"
          aria-modal="true"
          aria-label={`${server.name} details`}
          tabIndex={-1}
        >
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
                  className={styles.authHeadersBtn}
                  onClick={openHeaderEditor}
                >
                  {existingKeyCount > 0 ? `Auth (${existingKeyCount})` : 'Auth headers'}
                </button>
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

          {showHeaderEditor && (
            <div className={styles.headerEditor}>
              <div className={styles.headerEditorTitle}>Authentication headers</div>
              <p className={styles.headerEditorHint}>
                Existing values are masked. Leave a value as-is to preserve it, type a new value to overwrite, or remove a row to delete the header.
              </p>
              {headerRows.map((row, index) => (
                <div key={index} className={styles.headerEditorRow}>
                  <input
                    type="text"
                    className={styles.headerEditorKey}
                    placeholder="Header name"
                    value={row.key}
                    onChange={(e) => updateKey(index, e.target.value)}
                    aria-label={`Header name ${index + 1}`}
                  />
                  <input
                    type="text"
                    className={styles.headerEditorValue}
                    placeholder="Value"
                    value={row.value}
                    onChange={(e) => updateValue(index, e.target.value)}
                    aria-label={`Header value ${index + 1}`}
                  />
                  <button
                    type="button"
                    className={styles.headerEditorRemove}
                    onClick={() => removeHeaderRow(index)}
                    aria-label={`Remove header ${index + 1}`}
                  >
                    &times;
                  </button>
                </div>
              ))}
              <button type="button" className={styles.addHeaderBtn} onClick={addHeaderRow}>
                + Add header
              </button>
              {updateMutation.error && (
                <div className={styles.headerEditorError}>
                  {updateMutation.error.message}
                </div>
              )}
              <div className={styles.headerEditorFooter}>
                <button
                  type="button"
                  className={styles.cancelBtn}
                  onClick={() => { setShowHeaderEditor(false); updateMutation.reset() }}
                >
                  Cancel
                </button>
                <button
                  type="button"
                  className={styles.saveBtn}
                  onClick={handleSaveHeaders}
                  disabled={updateMutation.isPending}
                >
                  {updateMutation.isPending ? 'Saving…' : 'Save'}
                </button>
              </div>
            </div>
          )}

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
    </FocusTrap>
  )
}
