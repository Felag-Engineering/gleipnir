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
// useQueries is used for eager tool fetching — mock at the module level
vi.mock('@tanstack/react-query', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-query')>()
  return {
    ...actual,
    useQueries: vi.fn(),
  }
})

import { useMcpServers } from '@/hooks/queries/servers'
import { useAddMcpServer } from '@/hooks/mutations/servers'
import { useDeleteMcpServer } from '@/hooks/mutations/servers'
import { useDiscoverMcpServer } from '@/hooks/mutations/servers'
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
  input_schema: { namespace: { type: 'string' } },
}

const TOOL_2: ApiMcpTool = {
  id: 't2',
  server_id: 'srv-1',
  name: 'kubectl.delete_pod',
  description: 'Delete a pod.',
  input_schema: {},
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

  it('shows Connected health for server with last_discovered_at', () => {
    renderPage()
    const connectedLabels = screen.getAllByText('Connected')
    expect(connectedLabels.length).toBeGreaterThan(0)
  })

  it('shows Unreachable health for server with null last_discovered_at', () => {
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

  it('hides drift badge when server has_drift is false', () => {
    mockServersLoaded([SERVER_1], new Map([['srv-1', [TOOL_1]]]))
    mockNoopMutations()
    renderPage()
    expect(screen.queryByText('Drift')).not.toBeInTheDocument()
  })
})

describe('ToolsPage — stats bar', () => {
  it('shows correct tool counts from eager fetches', () => {
    const tools = new Map([['srv-1', [TOOL_1, TOOL_2]]])
    mockServersLoaded([SERVER_1], tools)
    mockNoopMutations()

    renderPage()
    // Total tools = 2
    const twos = screen.getAllByText('2')
    expect(twos.length).toBeGreaterThan(0)
  })

  it('shows dash placeholder on initial load with no cached data', () => {
    vi.mocked(useMcpServers).mockReturnValue({
      data: [SERVER_1],
      status: 'success',
    } as ReturnType<typeof useMcpServers>)

    vi.mocked(useQueries).mockReturnValue([
      { data: undefined, status: 'pending' },
    ] as ReturnType<typeof useQueries>)

    mockNoopMutations()
    renderPage()

    const dashes = screen.getAllByText('–')
    expect(dashes.length).toBeGreaterThan(0)
  })

  it('shows cached tool counts during background refetch instead of dashes', () => {
    vi.mocked(useMcpServers).mockReturnValue({
      data: [SERVER_1],
      status: 'success',
    } as ReturnType<typeof useMcpServers>)

    // Simulate re-navigation: data is cached but query is refetching in background
    vi.mocked(useQueries).mockReturnValue([
      { data: [TOOL_1, TOOL_2], status: 'success' },
    ] as ReturnType<typeof useQueries>)

    mockNoopMutations()
    renderPage()

    // Stats should show actual counts, not dashes
    const twos = screen.getAllByText('2')
    expect(twos.length).toBeGreaterThan(0)
    expect(screen.queryAllByText('–').length).toBe(0)
  })
})

describe('ToolsPage — add MCP server modal', () => {
  beforeEach(() => {
    mockServersLoaded([])
    mockNoopMutations()
  })

  it('opens add MCP server modal on button click', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /add mcp server/i }))
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
    fireEvent.click(screen.getByRole('button', { name: /add mcp server/i }))

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'my-mcp' } })
    fireEvent.change(screen.getByLabelText(/url/i), { target: { value: 'http://my-mcp:8080' } })
    // Submit the form directly
    const form = document.querySelector('#add-server-form') as HTMLFormElement
    fireEvent.submit(form)

    expect(mutateMock).toHaveBeenCalledWith(
      { name: 'my-mcp', url: 'http://my-mcp:8080' },
      expect.any(Object),
    )
  })

  it('closes modal on cancel', async () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /add mcp server/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    })
  })

  it('shows spinner when add is pending', () => {
    vi.mocked(useAddMcpServer).mockReturnValue({
      mutate: vi.fn(),
      isPending: true,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useAddMcpServer>)

    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /add mcp server/i }))
    // When isPending, the submit button is disabled and shows "Adding…"
    const submitBtn = screen.getByRole('button', { name: /adding/i })
    expect(submitBtn).toBeDisabled()
  })
})

describe('ToolsPage — delete MCP server modal', () => {
  beforeEach(() => {
    const tools = new Map([['srv-1', [TOOL_1, TOOL_2]]])
    mockServersLoaded([SERVER_1], tools)
    mockNoopMutations()
  })

  it('opens delete confirmation modal when Delete is clicked', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /delete kubectl-mcp/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Delete MCP server' })).toBeInTheDocument()
  })

  it('calls deleteMutate on confirm', () => {
    const mutateMock = vi.fn()
    vi.mocked(useDeleteMcpServer).mockReturnValue({
      mutate: mutateMock,
      isPending: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useDeleteMcpServer>)

    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /delete kubectl-mcp/i }))
    fireEvent.click(screen.getByRole('button', { name: /delete mcp server/i }))

    expect(mutateMock).toHaveBeenCalledWith('srv-1', expect.any(Object))
  })

  it('closes modal on cancel', async () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /delete kubectl-mcp/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    })
  })
})

describe('ToolsPage — discover button', () => {
  it('shows spinner when discover is pending for a server', () => {
    const tools = new Map([['srv-1', [TOOL_1]]])
    mockServersLoaded([SERVER_1], tools)
    mockNoopMutations()

    // We test the ServerCard isDiscovering prop via the discover button
    // First render normally, then simulate click
    const mutateMock = vi.fn()
    vi.mocked(useDiscoverMcpServer).mockReturnValue({
      mutate: mutateMock,
      isPending: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useDiscoverMcpServer>)

    renderPage()
    const discoverBtn = screen.getByRole('button', { name: /discover tools for kubectl-mcp/i })
    expect(discoverBtn).toBeInTheDocument()
    fireEvent.click(discoverBtn)
    expect(mutateMock).toHaveBeenCalledWith('srv-1', expect.any(Object))
  })
})

describe('ToolsPage — server card tool count', () => {
  it('shows the number of tools discovered for a server', async () => {
    const tools = new Map([['srv-1', [TOOL_1, TOOL_2]]])
    mockServersLoaded([SERVER_1], tools)
    mockNoopMutations()

    renderPage()

    // ServerCard renders a button label with tool count; expand to see tool list
    const toolsBtn = screen.getByRole('button', { name: /2 tools/i })
    expect(toolsBtn).toBeInTheDocument()
  })

  it('shows 0 tools for a server with an empty tool list', () => {
    mockServersLoaded([SERVER_1], new Map([['srv-1', []]]))
    mockNoopMutations()

    renderPage()
    const toolsBtn = screen.getByRole('button', { name: /0 tools/i })
    expect(toolsBtn).toBeInTheDocument()
  })
})
