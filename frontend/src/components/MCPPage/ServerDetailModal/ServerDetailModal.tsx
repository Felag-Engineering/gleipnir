import { useEffect, useMemo, useState } from 'react'
import FocusTrap from 'focus-trap-react'
import { Search } from 'lucide-react'
import type { ApiMcpServer, ApiMcpTool, ApiPolicyListItem } from '@/api/types'
import { explainArgEnforcement } from '@/components/MCPPage/ArgEnforcementBadge'
import { ToolDetail } from '@/components/MCPPage/ToolDetail'
import { topLevelParamCount } from '@/components/MCPPage/ToolDetail/schemaParams'
import { Tabs, tabId, panelId } from '@/components/Tabs'
import { SkeletonBlock } from '@/components/SkeletonBlock'
import { formatTimeAgo } from '@/utils/format'
import {
  useSetMcpServerHeader,
  useDeleteMcpServerHeader,
  useSetMcpToolEnabled,
} from '@/hooks/mutations/servers'
import { useToast } from '@/components/Toast'
import { ArcadeAuthSection } from './ArcadeAuthSection'
import { CaCertificateSection } from './CaCertificateSection'
import { CallTimeoutSection } from './CallTimeoutSection'
import { RunAttributionSection } from './RunAttributionSection'
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
  const toast = useToast()
  const [activeTab, setActiveTab] = useState<'tools' | 'connection'>('tools')
  const [selectedToolId, setSelectedToolId] = useState<string | null>(null)
  const [filter, setFilter] = useState('')
  const [showHeaderEditor, setShowHeaderEditor] = useState(false)
  const [headerRows, setHeaderRows] = useState<HeaderRow[]>([])
  const [isSaving, setIsSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  const setHeaderMutation = useSetMcpServerHeader()
  const deleteHeaderMutation = useDeleteMcpServerHeader()

  const toolCount = tools?.length ?? 0
  // A managed entry is a plugin instance's endpoint. It resolves through the
  // same client stack as any other server, but its URL, its name, and its
  // credentials belong to the plugin lifecycle rather than to an admin here.
  const isManaged = server.trust_tier === 'managed'
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

  // The selected tool falls back to the first visible one, so the detail
  // pane is never empty while the list has entries — including after the
  // filter hides the previous selection.
  const selectedTool =
    filteredTools?.find((t) => t.id === selectedToolId) ?? filteredTools?.[0]

  const showFilter = !toolsLoading && toolCount > 5
  const existingKeys = server.auth_header_keys ?? []
  const idPrefix = `server-${server.id}`

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
                {isManaged && (
                  <span className={styles.managedBadge} title="Managed by a plugin's lifecycle">
                    Plugin
                  </span>
                )}
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
                <span className={styles.url}>{server.url}</span>
                {server.last_discovered_at && (
                  <>
                    <span className={styles.metaSep} aria-hidden="true">&middot;</span>
                    <span>Discovered {formatTimeAgo(server.last_discovered_at)}</span>
                  </>
                )}
              </div>
              <div className={styles.actions}>
                {/*
                  A managed entry's lifecycle owns the row, so Delete is not
                  offered — the API answers it with a 409, and an affordance
                  that always fails is worse than no affordance. Rediscover
                  stays: it is a read.
                */}
                {isManaged && (
                  <span className={styles.managedNote}>
                    Managed by the plugin lifecycle
                  </span>
                )}
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
                {!isManaged && (
                  <button
                    type="button"
                    className={styles.deleteBtn}
                    onClick={() => onDelete(server, toolCount)}
                  >
                    Delete
                  </button>
                )}
              </div>
            </div>
          </div>

          <div className={styles.tabs}>
            <Tabs
              tabs={[
                { id: 'tools', label: `Tools (${toolCount})` },
                { id: 'connection', label: 'Connection' },
              ]}
              activeId={activeTab}
              onChange={(id) => setActiveTab(id as 'tools' | 'connection')}
              ariaLabel={`${server.name} sections`}
              idPrefix={idPrefix}
              showMarkers={false}
            />
          </div>

          {/*
            Both panels stay mounted so an in-progress edit on the Connection
            tab survives a look at the tools. Each panel owns its own scrolling:
            the modal itself never scrolls, so the header and tabs stay put.
          */}
          <div
            className={styles.toolsPanel}
            role="tabpanel"
            id={panelId(idPrefix, 'tools')}
            aria-labelledby={tabId(idPrefix, 'tools')}
            hidden={activeTab !== 'tools'}
          >
            {toolsLoading ? (
              <div className={styles.loadingContainer}>
                <SkeletonBlock height={44} />
                <SkeletonBlock height={44} />
                <SkeletonBlock height={44} />
              </div>
            ) : toolCount === 0 ? (
              <div className={styles.empty}>
                No tools discovered. Click Rediscover to fetch tools from this server.
              </div>
            ) : (
              <div className={styles.toolsLayout}>
                <div className={styles.listPane}>
                  {showFilter && (
                    <div className={styles.filterBar}>
                      <Search size={14} className={styles.filterIcon} aria-hidden="true" />
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
                  {filteredTools && filteredTools.length > 0 ? (
                    <ul className={styles.toolList} aria-label="Tools">
                      {filteredTools.map((tool) => {
                        const selected = tool.id === selectedTool?.id
                        const paramCount = topLevelParamCount(tool.input_schema)
                        return (
                          <li key={tool.id}>
                            <button
                              type="button"
                              className={`${styles.toolItem} ${selected ? styles.toolItemSelected : ''} ${tool.enabled === false ? styles.toolItemDisabled : ''}`}
                              aria-current={selected ? 'true' : undefined}
                              onClick={() => setSelectedToolId(tool.id)}
                            >
                              <span className={styles.toolItemName}>{tool.name}</span>
                              <span className={styles.toolItemMeta}>
                                {tool.enabled === false && (
                                  <span className={styles.toolItemDisabledTag}>Disabled</span>
                                )}
                                {explainArgEnforcement(tool.arg_enforcement) && (
                                  <span
                                    className={styles.toolItemDisabledTag}
                                    title="Argument values are not fully checked for this tool"
                                  >
                                    Reduced checking
                                  </span>
                                )}
                                {paramCount === 0 ? 'no params' : `${paramCount} param${paramCount === 1 ? '' : 's'}`}
                              </span>
                            </button>
                          </li>
                        )
                      })}
                    </ul>
                  ) : (
                    <div className={styles.empty}>No tools matching "{filter}"</div>
                  )}
                </div>
                <div className={styles.detailPane}>
                  {selectedTool && (
                    <ToolDetail
                      // Keyed so the JSON viewer's expanded state resets per tool.
                      key={selectedTool.id}
                      tool={selectedTool}
                      serverName={server.name}
                      usedBy={toolUsageMap.get(`${server.name}.${selectedTool.name}`)}
                      canManage={canManage}
                      onSetEnabled={(enabled) =>
                        setToolEnabledMutation.mutate(
                          { serverId: server.id, toolId: selectedTool.id, enabled },
                          {
                            // No success toast — the tool's status pill reflects the new state.
                            // Toggling several tools in a row shouldn't stack confirmations.
                            onError: () => toast.error("Couldn't update tool"),
                          },
                        )
                      }
                      isUpdatingEnabled={
                        setToolEnabledMutation.isPending &&
                        setToolEnabledMutation.variables?.toolId === selectedTool.id
                      }
                    />
                  )}
                </div>
              </div>
            )}
          </div>

          <div
            className={styles.connectionPanel}
            role="tabpanel"
            id={panelId(idPrefix, 'connection')}
            aria-labelledby={tabId(idPrefix, 'connection')}
            hidden={activeTab !== 'connection'}
          >
            <section className={styles.connSection}>
              <h3 className={styles.connTitle}>Authentication headers</h3>
              {isManaged ? (
                // A managed entry's credentials come from the plugin's own
                // credential surface; the API answers header writes with a 409.
                <p className={styles.connHint}>
                  Credentials for this server come from its plugin and are managed there.
                </p>
              ) : showHeaderEditor ? (
                <div className={styles.headerEditor}>
                  <p className={styles.connHint}>
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
              ) : (
                <>
                  {existingKeys.length > 0 ? (
                    <div className={styles.headerKeys}>
                      {existingKeys.map((k) => (
                        <code key={k} className={styles.headerKey}>{k}</code>
                      ))}
                    </div>
                  ) : (
                    <p className={styles.connHint}>No authentication headers are sent to this server.</p>
                  )}
                  <div>
                    <button type="button" className={styles.secondaryBtn} onClick={openHeaderEditor}>
                      {existingKeys.length > 0 ? 'Edit headers' : 'Add headers'}
                    </button>
                  </div>
                </>
              )}
            </section>

            {!isManaged && <CaCertificateSection server={server} />}

            <CallTimeoutSection server={server} />

            <RunAttributionSection server={server} />

            {server.is_arcade_gateway && tools && (
              <ArcadeAuthSection
                server={server}
                tools={tools}
                canManage={canManage}
              />
            )}
          </div>
        </div>
      </div>
    </FocusTrap>
  )
}
