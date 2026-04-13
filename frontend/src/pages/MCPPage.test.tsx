import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

import MCPPage from './MCPPage'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'

// --- Mocks ---

vi.mock('@/hooks/queries/servers')
vi.mock('@/hooks/mutations/servers')
vi.mock('@tanstack/react-query', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-query')>()
  return {
    ...actual,
    useQueries: vi.fn(),
  }
})

import { useMcpServers } from '@/hooks/queries/servers'
import { useAddMcpServer, useDeleteMcpServer, useDiscoverMcpServer, useTestMcpConnection } from '@/hooks/mutations/servers'
import { useQueries } from '@tanstack/react-query'

// --- Fixtures ---

const SERVER_1: ApiMcpServer = {
  id: 'srv-1',
  name: 'kubectl-mcp',
  url: 'http://kubectl-mcp:8080',
  last_discovered_at: '2026-03-10T12:00:00Z',
  has_drift: false,
  created_at: '2026-03-01T00:00:00Z',
}

const SERVER_2: ApiMcpServer = {
  id: 'srv-2',
  name: 'vikunja-mcp',
  url: 'http://vikunja-mcp:8080',
  last_discovered_at: null,
  has_drift: false,
  created_at: '2026-03-02T00:00:00Z',
}

const TOOL_1: ApiMcpTool = {
  id: 't1',
  server_id: 'srv-1',
  name: 'kubectl.get_pods',
  description: 'List pods.',
  input_schema: {
    properties: { namespace: { type: 'string' } },
    required: ['namespace'],
    type: 'object',
  },
}

const TOOL_2: ApiMcpTool = {
  id: 't2',
  server_id: 'srv-1',
  name: 'kubectl.delete_pod',
  description: 'Delete a pod.',
  input_schema: { properties: {}, type: 'object' },
}

// --- Helpers ---

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function renderPage(queryClient = makeQueryClient()) {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <MCPPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function mockNoopMutations() {
  const noop = { mutate: vi.fn(), isPending: false, error: null, reset: vi.fn() }
  vi.mocked(useAddMcpServer).mockReturnValue(noop as unknown as ReturnType<typeof useAddMcpServer>)
  vi.mocked(useDeleteMcpServer).mockReturnValue(noop as unknown as ReturnType<typeof useDeleteMcpServer>)
  vi.mocked(useDiscoverMcpServer).mockReturnValue(noop as unknown as ReturnType<typeof useDiscoverMcpServer>)
  vi.mocked(useTestMcpConnection).mockReturnValue(noop as unknown as ReturnType<typeof useTestMcpConnection>)
}

function mockServersLoaded(servers: ApiMcpServer[], toolsByServer: Map<string, ApiMcpTool[]> = new Map()) {
  vi.mocked(useMcpServers).mockReturnValue({
    data: servers,
    status: 'success',
  } as ReturnType<typeof useMcpServers>)

  vi.mocked(useQueries).mockReturnValue(
    servers.map((s) => ({
      data: toolsByServer.get(s.id) ?? [],
      status: 'success',
    })) as ReturnType<typeof useQueries>,
  )
}

function mockServersPending() {
  vi.mocked(useMcpServers).mockReturnValue({
    data: undefined,
    status: 'pending',
  } as ReturnType<typeof useMcpServers>)

  vi.mocked(useQueries).mockReturnValue([] as ReturnType<typeof useQueries>)
}

// --- Tests ---

describe('ToolsPage — skeleton on load', () => {
  beforeEach(() => {
    mockServersPending()
    mockNoopMutations()
  })

  it('shows skeleton blocks while servers are loading', () => {
    renderPage()
    const skeletons = document.querySelectorAll('[aria-hidden="true"]')
    expect(skeletons.length).toBeGreaterThan(0)
  })

  it('does not show server names while loading', () => {
    renderPage()
    expect(screen.queryByText('kubectl-mcp')).not.toBeInTheDocument()
  })
})

describe('ToolsPage — servers loaded', () => {
  beforeEach(() => {
    const tools = new Map([['srv-1', [TOOL_1, TOOL_2]]])
    mockServersLoaded([SERVER_1, SERVER_2], tools)
    mockNoopMutations()
  })

  it('renders server names', () => {
    renderPage()
    expect(screen.getByText('kubectl-mcp')).toBeInTheDocument()
    expect(screen.getByText('vikunja-mcp')).toBeInTheDocument()
  })

  it('renders server URLs', () => {
    renderPage()
    expect(screen.getByText('http://kubectl-mcp:8080')).toBeInTheDocument()
    expect(screen.getByText('http://vikunja-mcp:8080')).toBeInTheDocument()
  })

  it('shows no status badge for healthy connected server', () => {
    renderPage()
    expect(screen.queryByText('Connected')).not.toBeInTheDocument()
  })

  it('shows Unreachable badge for server with null last_discovered_at', () => {
    renderPage()
    expect(screen.getByText('Unreachable')).toBeInTheDocument()
  })

  it('shows drift badge when server has_drift is true', () => {
    const driftedServer = { ...SERVER_1, has_drift: true }
    mockServersLoaded([driftedServer], new Map([['srv-1', [TOOL_1]]]))
    mockNoopMutations()
    renderPage()
    expect(screen.getByText('Drift')).toBeInTheDocument()
  })

  it('shows tool name chips on server card', () => {
    renderPage()
    expect(screen.getByText('kubectl.get_pods')).toBeInTheDocument()
    expect(screen.getByText('kubectl.delete_pod')).toBeInTheDocument()
  })

  it('shows tool count badge', () => {
    renderPage()
    expect(screen.getByText('2 tools')).toBeInTheDocument()
  })
})

describe('ToolsPage — server detail modal', () => {
  beforeEach(() => {
    const tools = new Map([['srv-1', [TOOL_1, TOOL_2]]])
    mockServersLoaded([SERVER_1], tools)
    mockNoopMutations()
  })

  it('opens modal when server card is clicked', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /kubectl-mcp/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getAllByText('kubectl-mcp').length).toBeGreaterThan(0)
  })

  it('shows tool accordion in modal', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /kubectl-mcp/i }))
    expect(screen.getAllByText('kubectl.get_pods').length).toBeGreaterThan(0)
    expect(screen.getAllByText('kubectl.delete_pod').length).toBeGreaterThan(0)
  })

  it('closes modal when close button is clicked', async () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /kubectl-mcp/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()

    fireEvent.click(screen.getByLabelText('Close'))
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    })
  })

  it('triggers discover from modal', () => {
    const mutateMock = vi.fn()
    vi.mocked(useDiscoverMcpServer).mockReturnValue({
      mutate: mutateMock,
      isPending: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useDiscoverMcpServer>)

    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /kubectl-mcp/i }))
    fireEvent.click(screen.getByRole('button', { name: /rediscover/i }))
    expect(mutateMock).toHaveBeenCalledWith('srv-1', expect.any(Object))
  })

  it('triggers delete from modal', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /kubectl-mcp/i }))
    // Use exact match to avoid matching tool buttons that contain "delete" in their name
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(screen.getByRole('heading', { name: 'Delete MCP server' })).toBeInTheDocument()
  })
})

describe('ToolsPage — add MCP server modal', () => {
  beforeEach(() => {
    mockServersLoaded([])
    mockNoopMutations()
  })

  it('opens add MCP server modal on button click', () => {
    renderPage()
    // Two buttons exist (header + empty state); click the first (header)
    fireEvent.click(screen.getAllByRole('button', { name: /add mcp server/i })[0])
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Add MCP server' })).toBeInTheDocument()
  })

  it('calls mutate with name and url on submit', () => {
    const mutateMock = vi.fn()
    vi.mocked(useAddMcpServer).mockReturnValue({
      mutate: mutateMock,
      isPending: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useAddMcpServer>)

    renderPage()
    fireEvent.click(screen.getAllByRole('button', { name: /add mcp server/i })[0])

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'my-mcp' } })
    fireEvent.change(screen.getByLabelText(/url/i), { target: { value: 'http://my-mcp:8080' } })
    const form = document.querySelector('#add-server-form') as HTMLFormElement
    fireEvent.submit(form)

    expect(mutateMock).toHaveBeenCalledWith(
      { name: 'my-mcp', url: 'http://my-mcp:8080' },
      expect.any(Object),
    )
  })
})

describe('ToolsPage — empty state', () => {
  beforeEach(() => {
    mockServersLoaded([])
    mockNoopMutations()
  })

  it('shows empty state with add button when no servers exist', () => {
    renderPage()
    expect(screen.getByText('No MCP servers')).toBeInTheDocument()
    expect(screen.getByText('Add an MCP server to start discovering tools.')).toBeInTheDocument()
    const addButtons = screen.getAllByRole('button', { name: /add mcp server/i })
    expect(addButtons.length).toBe(2) // header + empty state
  })
})
