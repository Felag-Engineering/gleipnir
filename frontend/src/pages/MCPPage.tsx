import { useState } from 'react'
import { useQueries } from '@tanstack/react-query'
import { useMcpServers } from '@/hooks/useMcpServers'
import { queryKeys } from '@/hooks/queryKeys'
import { useAddMcpServer } from '@/hooks/useAddMcpServer'
import { useDeleteMcpServer } from '@/hooks/useDeleteMcpServer'
import { useDiscoverMcpServer } from '@/hooks/useDiscoverMcpServer'
import { useUpdateMcpTool } from '@/hooks/useUpdateMcpTool'
import { apiFetch } from '@/api/fetch'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'
import type { ApiError } from '@/api/fetch'
import { SkeletonBlock } from '@/components/SkeletonBlock'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { MCPStatsBar } from '@/components/MCPPage/MCPStatsBar'
import { UnassignedBanner } from '@/components/MCPPage/UnassignedBanner'
import { ServerCard } from '@/components/MCPPage/ServerCard'
import { AddServerModal } from '@/components/MCPPage/AddServerModal'
import { DeleteServerModal } from '@/components/MCPPage/DeleteServerModal'
import styles from './MCPPage.module.css'

interface DeleteTarget {
  server: ApiMcpServer
  toolCount: number
}

export default function MCPPage() {
  const [showAddModal, setShowAddModal] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<DeleteTarget | null>(null)
  const [addDiscoveryWarning, setAddDiscoveryWarning] = useState<string | null>(null)
  const [updatingToolId, setUpdatingToolId] = useState<string | null>(null)
  const [discoveringServerId, setDiscoveringServerId] = useState<string | null>(null)

  const { data: servers, status: serversStatus } = useMcpServers()

  // Eagerly fetch all server tool lists so stats are accurate.
  const toolResults = useQueries({
    queries: (servers ?? []).map((server) => ({
      queryKey: queryKeys.servers.tools(server.id),
      queryFn: () => apiFetch<ApiMcpTool[]>(`/mcp/servers/${encodeURIComponent(server.id)}/tools`),
      enabled: Boolean(server.id),
    })),
  })

  const addMutation = useAddMcpServer()
  const deleteMutation = useDeleteMcpServer()
  const discoverMutation = useDiscoverMcpServer()
  const updateToolMutation = useUpdateMcpTool()

  // Build per-server tool map from eager queries
  const toolsByServer = new Map<string, ApiMcpTool[]>()
  ;(servers ?? []).forEach((server, i) => {
    const result = toolResults[i]
    if (result?.data) {
      toolsByServer.set(server.id, result.data)
    }
  })

  // Compute stats from all loaded tools
  const allTools = Array.from(toolsByServer.values()).flat()
  const toolsFullyLoaded = toolResults.every((r) => r.status !== 'pending')
  const sensors = allTools.filter((t) => t.capability_role === 'sensor').length
  const actuators = allTools.filter((t) => t.capability_role === 'actuator').length
  const feedback = allTools.filter((t) => t.capability_role === 'feedback').length

  // Unassigned: tools that have no valid role (defensive, DB constraint prevents this normally)
  const unassignedCount = allTools.filter(
    (t) => !['sensor', 'actuator', 'feedback'].includes(t.capability_role),
  ).length

  function handleAddSubmit(name: string, url: string) {
    setAddDiscoveryWarning(null)
    addMutation.mutate(
      { name, url },
      {
        onSuccess: (data) => {
          if (data.discovery_error) {
            setAddDiscoveryWarning(data.discovery_error)
          } else {
            setShowAddModal(false)
            addMutation.reset()
          }
        },
      },
    )
  }

  function handleAddClose() {
    setShowAddModal(false)
    setAddDiscoveryWarning(null)
    addMutation.reset()
  }

  function handleDeleteOpen(server: ApiMcpServer, toolCount: number) {
    setDeleteTarget({ server, toolCount })
    deleteMutation.reset()
  }

  function handleDeleteConfirm() {
    if (!deleteTarget) return
    deleteMutation.mutate(deleteTarget.server.id, {
      onSuccess: () => {
        setDeleteTarget(null)
      },
    })
  }

  function handleDeleteClose() {
    setDeleteTarget(null)
    deleteMutation.reset()
  }

  function handleDiscover(serverId: string) {
    setDiscoveringServerId(serverId)
    discoverMutation.mutate(serverId, {
      onSettled: () => setDiscoveringServerId(null),
    })
  }

  function handleRoleChange(toolId: string, serverId: string, role: 'sensor' | 'actuator' | 'feedback') {
    setUpdatingToolId(toolId)
    updateToolMutation.mutate(
      { toolId, serverId, capability_role: role },
      { onSettled: () => setUpdatingToolId(null) },
    )
  }

  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <h1 className={styles.title}>Tools</h1>
        <button
          type="button"
          className={styles.addBtn}
          onClick={() => {
            setAddDiscoveryWarning(null)
            addMutation.reset()
            setShowAddModal(true)
          }}
        >
          Add MCP server
        </button>
      </div>

      <ErrorBoundary>
        <MCPStatsBar
          totalTools={allTools.length}
          sensors={sensors}
          actuators={actuators}
          feedback={feedback}
          isLoading={!toolsFullyLoaded}
        />

        {unassignedCount > 0 && <UnassignedBanner count={unassignedCount} />}

        {serversStatus === 'pending' && (
          <div className={styles.skeletonList}>
            <SkeletonBlock height={120} borderRadius={8} />
            <SkeletonBlock height={120} borderRadius={8} />
            <SkeletonBlock height={120} borderRadius={8} />
          </div>
        )}

        {serversStatus === 'error' && (
          <div className={styles.errorState}>
            Failed to load MCP servers.
          </div>
        )}

        {serversStatus === 'success' && servers.length === 0 && (
          <div className={styles.emptyState}>
            <p className={styles.emptyHeadline}>No MCP servers</p>
            <p className={styles.emptySubtext}>Add an MCP server to start discovering tools.</p>
          </div>
        )}

        {serversStatus === 'success' && servers.length > 0 && (
          <div className={styles.serverList}>
            {servers.map((server, i) => {
              const toolResult = toolResults[i]
              return (
                <ServerCard
                  key={server.id}
                  server={server}
                  tools={toolsByServer.get(server.id)}
                  toolsLoading={toolResult?.status === 'pending'}
                  isDiscovering={discoveringServerId === server.id}
                  onDiscover={handleDiscover}
                  onDelete={handleDeleteOpen}
                  onRoleChange={handleRoleChange}
                  updatingToolId={updatingToolId}
                />
              )
            })}
          </div>
        )}
      </ErrorBoundary>

      {showAddModal && (
        <AddServerModal
          onClose={handleAddClose}
          onSubmit={handleAddSubmit}
          isPending={addMutation.isPending}
          error={addMutation.error as ApiError | null}
          discoveryWarning={addDiscoveryWarning}
        />
      )}

      {deleteTarget && (
        <DeleteServerModal
          serverName={deleteTarget.server.name}
          toolCount={deleteTarget.toolCount}
          onClose={handleDeleteClose}
          onConfirm={handleDeleteConfirm}
          isPending={deleteMutation.isPending}
          error={deleteMutation.error as ApiError | null}
        />
      )}
    </div>
  )
}
