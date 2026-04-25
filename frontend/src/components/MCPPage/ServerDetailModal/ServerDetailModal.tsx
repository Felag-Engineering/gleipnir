import { useEffect, useMemo, useState } from 'react'
import FocusTrap from 'focus-trap-react'
import { Search } from 'lucide-react'
import type { ApiMcpServer, ApiMcpTool, ApiPolicyListItem } from '@/api/types'
import { ToolAccordionRow } from '@/components/MCPPage/ToolAccordionRow'
import { SkeletonBlock } from '@/components/SkeletonBlock'
import { formatTimeAgo } from '@/utils/format'
import {
  useUpdateMcpServer,
  useSetMcpServerHeader,
  useDeleteMcpServerHeader,
  useSetMcpToolEnabled,
} from '@/hooks/mutations/servers'
import styles from './ServerDetailModal.module.css'

// A row in the header editor.
// originalName is set for rows loaded from the server; absent for newly-added rows.
// value is always empty for existing rows until the operator types a replacement.
interface HeaderRow {
  originalName?: string
  name: string
  value: string
}

interface Props {
  server: ApiMcpServer
  tools: ApiMcpTool[] | undefined
  toolsLoading: boolean
  isDiscovering: boolean
  policies: ApiPolicyListItem[] | undefined
  // canManage controls whether enable/disable toggles are shown.
  // Derived from the current user's roles in MCPPage and passed as a prop
  // to keep this component controlled and prop-driven.
  canManage: boolean
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
  canManage,
  onClose,
  onDiscover,
  onDelete,
}: Props) {
  const setToolEnabledMutation = useSetMcpToolEnabled()
  const [expandedToolId, setExpandedToolId] = useState<string | null>(null)
  const [filter, setFilter] = useState('')
  const [showHeaderEditor, setShowHeaderEditor] = useState(false)
  const [headerRows, setHeaderRows] = useState<HeaderRow[]>([])
  const [isSaving, setIsSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  const updateMutation = useUpdateMcpServer()
  const setHeaderMutation = useSetMcpServerHeader()
  const deleteHeaderMutation = useDeleteMcpServerHeader()

  const toolCount = tools?.length ?? 0
  const isUnreachable = server.last_discovered_at === null
  const hasDrift = server.has_drift

  // Seed the header editor from server.auth_header_keys when it opens.
  // Existing rows have an empty value field — the operator must type a new
  // value to replace it. A placeholder communicates that a value is stored.
  function openHeaderEditor() {
    const keys = server.auth_header_keys ?? []
    setHeaderRows(keys.map((k) => ({ originalName: k, name: k, value: '' })))
    setSaveError(null)
    setShowHeaderEditor(true)
  }

  function addHeaderRow() {
    setHeaderRows((prev) => [...prev, { originalName: undefined, name: '', value: '' }])
  }

  function removeHeaderRow(index: number) {
    setHeaderRows((prev) => prev.filter((_, i) => i !== index))
  }

  function updateValue(index: number, value: string) {
    setHeaderRows((prev) => prev.map((h, i) => (i === index ? { ...h, value } : h)))
  }

  function updateName(index: number, name: string) {
    setHeaderRows((prev) => prev.map((h, i) => (i === index ? { ...h, name } : h)))
  }

  async function handleSaveHeaders() {
    setSaveError(null)
    setIsSaving(true)

    const loadedKeys = server.auth_header_keys ?? []
    const currentNames = new Set(headerRows.map((r) => r.originalName).filter(Boolean) as string[])

    const promises: Promise<unknown>[] = []

    // Name or URL changed → update server metadata.
    if (headerRows.length === 0 || server.name !== server.name || server.url !== server.url) {
      // Only call updateMutation if name/url actually needs updating. The caller
      // of this component owns those fields, so we skip the update here unless
      // a dedicated name/url form fires it. The per-header endpoints are the
      // focus of this editor.
    }

    // Set headers: any row with a non-empty value fires SetAuthHeader.
    for (const row of headerRows) {
      if (row.name.trim() && row.value !== '') {
        const name = row.name.trim()
        promises.push(
          new Promise<void>((resolve, reject) => {
            setHeaderMutation.mutate(
              { id: server.id, name, value: row.value },
              { onSuccess: () => resolve(), onError: (e) => reject(e) },
            )
          }),
        )
      }
    }

    // Delete headers: any originalName no longer present in local rows.
    for (const original of loadedKeys) {
      if (!currentNames.has(original)) {
        const name = original
        promises.push(
          new Promise<void>((resolve, reject) => {
            deleteHeaderMutation.mutate(
              { id: server.id, name },
              { onSuccess: () => resolve(), onError: (e) => reject(e) },
            )
          }),
        )
      }
    }

    try {
      await Promise.all(promises)
      setShowHeaderEditor(false)
      setSaveError(null)
    } catch (err) {
      setSaveError(err instanceof Error ? err.message : 'Save failed')
    } finally {
      setIsSaving(false)
    }
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
                Existing header names are read-only. Type a new value to replace a stored secret, or remove a row to delete the header. Add a new row to create an additional header.
              </p>
              {headerRows.map((row, index) => (
                <div key={index} className={styles.headerEditorRow}>
                  <input
                    type="text"
                    className={styles.headerEditorKey}
                    placeholder="Header name"
                    value={row.name}
                    readOnly={row.originalName !== undefined}
                    disabled={row.originalName !== undefined}
                    onChange={(e) => updateName(index, e.target.value)}
                    aria-label={`Header name ${index + 1}`}
                  />
                  <input
                    type="text"
                    className={styles.headerEditorValue}
                    placeholder={row.originalName !== undefined ? '•••• (saved — type to replace)' : 'Value'}
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
              {saveError && (
                <div className={styles.headerEditorError}>
                  {saveError}
                </div>
              )}
              <div className={styles.headerEditorFooter}>
                <button
                  type="button"
                  className={styles.cancelBtn}
                  onClick={() => { setShowHeaderEditor(false); setSaveError(null) }}
                >
                  Cancel
                </button>
                <button
                  type="button"
                  className={styles.saveBtn}
                  onClick={handleSaveHeaders}
                  disabled={isSaving}
                >
                  {isSaving ? 'Saving…' : 'Save'}
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
                    canManage={canManage}
                    onSetEnabled={(enabled) =>
                      setToolEnabledMutation.mutate({ serverId: server.id, toolId: tool.id, enabled })
                    }
                    isUpdatingEnabled={
                      setToolEnabledMutation.isPending &&
                      setToolEnabledMutation.variables?.toolId === tool.id
                    }
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
