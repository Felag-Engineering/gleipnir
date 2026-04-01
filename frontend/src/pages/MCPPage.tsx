import { useState } from 'react'
import { useQueries } from '@tanstack/react-query'
import { useMcpServers } from '@/hooks/useMcpServers'
import { queryKeys } from '@/hooks/queryKeys'
import { useAddMcpServer } from '@/hooks/useAddMcpServer'
import { useDeleteMcpServer } from '@/hooks/useDeleteMcpServer'
import { useDiscoverMcpServer } from '@/hooks/useDiscoverMcpServer'
import { apiFetch } from '@/api/fetch'
import { usePageTitle } from '@/hooks/usePageTitle'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'
import type { ApiError } from '@/api/fetch'
import { QueryBoundary, SkeletonList } from '@/components/QueryBoundary'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { MCPStatsBar } from '@/components/MCPPage/MCPStatsBar'
import { ServerCard } from '@/components/MCPPage/ServerCard'
import { AddServerModal } from '@/components/MCPPage/AddServerModal'
import { DeleteServerModal } from '@/components/MCPPage/DeleteServerModal'
import { PageHeader } from '@/components/PageHeader'
import { Button } from '@/components/Button'
import styles from './MCPPage.module.css'

interface DeleteTarget {
  server: ApiMcpServer
  toolCount: number
}

export default function MCPPage() {
  usePageTitle('Tools')
  const [showAddModal, setShowAddModal] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<DeleteTarget | null>(null)
  const [addDiscoveryWarning, setAddDiscoveryWarning] = useState<string | null>(null)
  const [discoveringServerId, setDiscoveringServerId] = useState<string | null>(null)

  const { data: servers, status: serversStatus } = useMcpServers()

  // Eagerly fetch all server tool lists so the total count is accurate.
  const toolResults = useQueries({
    queries: (servers ?? []).map((server) => ({
      queryKey: queryKeys.servers.tools(server.id),
      queryFn: () => apiFetch<ApiMcpTool[]>(`/mcp/servers/${encodeURIComponent(server.id)}/tools`),
      enabled: Boolean(server.id),
      staleTime: 30_000,
    })),
  })

  const addMutation = useAddMcpServer()
  const deleteMutation = useDeleteMcpServer()
  const discoverMutation = useDiscoverMcpServer()

  // Build per-server tool map from eager queries
  const toolsByServer = new Map<string, ApiMcpTool[]>()
  ;(servers ?? []).forEach((server, i) => {
    const result = toolResults[i]
    if (result?.data) {
      toolsByServer.set(server.id, result.data)
    }
  })

  const allTools = Array.from(toolsByServer.values()).flat()
  // Show cached tool data immediately on re-navigation instead of showing
  // dashes while a background refetch is in flight. Only show loading state
  // when there is genuinely no data yet (first load).
  const toolsFullyLoaded = toolResults.length === 0 || toolResults.every((r) => r.data !== undefined)

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

  return (
    <div className={styles.page}>
      <PageHeader title="Tools">
        <Button
          variant="primary"
          onClick={() => {
            setAddDiscoveryWarning(null)
            addMutation.reset()
            setShowAddModal(true)
          }}
        >
          Add MCP server
        </Button>
      </PageHeader>

      <ErrorBoundary>
        <MCPStatsBar
          totalTools={allTools.length}
          isLoading={!toolsFullyLoaded}
        />

        <QueryBoundary
          status={serversStatus}
          isEmpty={(servers ?? []).length === 0}
          errorMessage="Failed to load MCP servers."
          skeleton={<SkeletonList count={3} height={120} gap={12} borderRadius={8} />}
          emptyState={
            <div className={styles.emptyState}>
              <p className={styles.emptyHeadline}>No MCP servers</p>
              <p className={styles.emptySubtext}>Add an MCP server to start discovering tools.</p>
            </div>
          }
        >
          <div className={styles.serverList}>
            {(servers ?? []).map((server, i) => {
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
                />
              )
            })}
          </div>
        </QueryBoundary>
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
