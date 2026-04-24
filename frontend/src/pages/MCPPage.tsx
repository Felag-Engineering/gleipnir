import { useState, useEffect } from 'react'
import { Wrench } from 'lucide-react'
import { useQueries } from '@tanstack/react-query'
import { useMcpServers } from '@/hooks/queries/servers'
import { queryKeys } from '@/hooks/queryKeys'
import { usePolicies } from '@/hooks/queries/policies'
import { useAddMcpServer, useDeleteMcpServer, useDiscoverMcpServer } from '@/hooks/mutations/servers'
import { useCurrentUser } from '@/hooks/queries/users'
import { apiFetch } from '@/api/fetch'
import { usePageTitle } from '@/hooks/usePageTitle'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'
import { QueryBoundary, SkeletonList } from '@/components/QueryBoundary'
import { ErrorBoundary } from '@/components/ErrorBoundary'
import { ServerCard } from '@/components/MCPPage/ServerCard'
import { ServerDetailModal } from '@/components/MCPPage/ServerDetailModal'
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
  const [selectedServer, setSelectedServer] = useState<ApiMcpServer | null>(null)

  const { data: servers, status: serversStatus } = useMcpServers()
  const { data: policies } = usePolicies()
  const { data: currentUser } = useCurrentUser()
  // canManage gates the enable/disable toggle — derived at page level so
  // ServerDetailModal stays a controlled, prop-driven component.
  const canManage =
    currentUser?.roles?.some((r) => r === 'admin' || r === 'operator') ?? false

  // Eagerly fetch all tools including disabled ones (toolsAll key) for the
  // management view. This uses a separate query key from queryKeys.servers.tools
  // so the policy form's enabled-only cache is never polluted with disabled tools.
  const toolResults = useQueries({
    queries: (servers ?? []).map((server) => ({
      queryKey: queryKeys.servers.toolsAll(server.id),
      queryFn: () =>
        apiFetch<ApiMcpTool[]>(
          `/mcp/servers/${encodeURIComponent(server.id)}/tools?include_disabled=true`,
        ),
      enabled: Boolean(server.id),
      staleTime: 30_000,
    })),
  })

  // Clear the discovering state once the tool query for the new server settles.
  useEffect(() => {
    if (!discoveringServerId || !servers) return
    const idx = servers.findIndex((s) => s.id === discoveringServerId)
    if (idx === -1) return
    const result = toolResults[idx]
    if (result && result.status !== 'pending') {
      setDiscoveringServerId(null)
    }
  }, [discoveringServerId, servers, toolResults])

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
            setDiscoveringServerId(data.id)
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
        // Close detail modal if the deleted server was open
        if (selectedServer?.id === deleteTarget.server.id) {
          setSelectedServer(null)
        }
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

  // Check whether tools are still loading for a given server.
  function isToolsLoading(serverId: string): boolean {
    const serverIndex = (servers ?? []).findIndex((s) => s.id === serverId)
    if (serverIndex === -1) return false
    return toolResults[serverIndex]?.status === 'pending'
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
        <QueryBoundary
          status={serversStatus}
          isEmpty={(servers ?? []).length === 0}
          errorMessage="Failed to load MCP servers."
          skeleton={<SkeletonList count={3} height={100} gap={12} borderRadius={8} />}
          emptyState={
            <div className={styles.emptyState}>
              <div className={styles.emptyIcon} aria-hidden="true">
                <Wrench size={48} />
              </div>
              <p className={styles.emptyHeadline}>No MCP servers</p>
              <p className={styles.emptySubtext}>Add an MCP server to start discovering tools.</p>
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
            </div>
          }
        >
          <div className={styles.serverList}>
            {(servers ?? []).map((server, i) => (
              <ServerCard
                key={server.id}
                server={server}
                tools={toolsByServer.get(server.id)}
                toolsLoading={isToolsLoading(server.id)}
                isDiscovering={discoveringServerId === server.id}
                onClick={() => setSelectedServer(server)}
              />
            ))}
          </div>
        </QueryBoundary>
      </ErrorBoundary>

      {selectedServer && (
        <ServerDetailModal
          server={selectedServer}
          tools={toolsByServer.get(selectedServer.id)}
          toolsLoading={isToolsLoading(selectedServer.id)}
          isDiscovering={discoveringServerId === selectedServer.id}
          policies={policies}
          canManage={canManage}
          onClose={() => setSelectedServer(null)}
          onDiscover={handleDiscover}
          onDelete={handleDeleteOpen}
        />
      )}

      {showAddModal && (
        <AddServerModal
          onClose={handleAddClose}
          onSubmit={handleAddSubmit}
          isPending={addMutation.isPending}
          error={addMutation.error}
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
          error={deleteMutation.error}
        />
      )}
    </div>
  )
}
