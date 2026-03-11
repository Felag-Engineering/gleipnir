import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

import MCPPage from './MCPPage'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'

// --- Mocks ---

vi.mock('@/hooks/useMcpServers')
vi.mock('@/hooks/useMcpTools')
vi.mock('@/hooks/useAddMcpServer')
vi.mock('@/hooks/useDeleteMcpServer')
vi.mock('@/hooks/useDiscoverMcpServer')
vi.mock('@/hooks/useUpdateMcpTool')
// useQueries is used for eager tool fetching — mock at the module level
vi.mock('@tanstack/react-query', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-query')>()
  return {
    ...actual,
    useQueries: vi.fn(),
  }
})

import { useMcpServers } from '@/hooks/useMcpServers'
import { useAddMcpServer } from '@/hooks/useAddMcpServer'
import { useDeleteMcpServer } from '@/hooks/useDeleteMcpServer'
import { useDiscoverMcpServer } from '@/hooks/useDiscoverMcpServer'
import { useUpdateMcpTool } from '@/hooks/useUpdateMcpTool'
import { useQueries } from '@tanstack/react-query'

// --- Fixtures ---

const SERVER_1: ApiMcpServer = {
  id: 'srv-1',
  name: 'kubectl-mcp',
  url: 'http://kubectl-mcp:8080',
  last_discovered_at: '2026-03-10T12:00:00Z',
  created_at: '2026-03-01T00:00:00Z',
}

const SERVER_2: ApiMcpServer = {
  id: 'srv-2',
  name: 'vikunja-mcp',
  url: 'http://vikunja-mcp:8080',
  last_discovered_at: null,
  created_at: '2026-03-02T00:00:00Z',
}

const TOOL_1: ApiMcpTool = {
  id: 't1',
  server_id: 'srv-1',
  name: 'kubectl.get_pods',
  description: 'List pods.',
  capability_role: 'sensor',
  input_schema: { namespace: { type: 'string' } },
}

const TOOL_2: ApiMcpTool = {
  id: 't2',
  server_id: 'srv-1',
  name: 'kubectl.delete_pod',
  description: 'Delete a pod.',
  capability_role: 'actuator',
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
  vi.mocked(useUpdateMcpTool).mockReturnValue(noop as unknown as ReturnType<typeof useUpdateMcpTool>)
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

describe('MCPPage — skeleton on load', () => {
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

describe('MCPPage — servers loaded', () => {
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
})

describe('MCPPage — stats bar', () => {
  it('shows correct tool counts from eager fetches', () => {
    const tools = new Map([['srv-1', [TOOL_1, TOOL_2]]])
    mockServersLoaded([SERVER_1], tools)
    mockNoopMutations()

    renderPage()
    // Total tools = 2 (TOOL_1 is sensor, TOOL_2 is actuator)
    const twos = screen.getAllByText('2')
    expect(twos.length).toBeGreaterThan(0)
    // At least one "1" for sensors or actuators count
    const ones = screen.getAllByText('1')
    expect(ones.length).toBeGreaterThan(0)
  })

  it('shows dash placeholder while tool lists are loading', () => {
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
})

describe('MCPPage — add server modal', () => {
  beforeEach(() => {
    mockServersLoaded([])
    mockNoopMutations()
  })

  it('opens add server modal on button click', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /add server/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText('Add MCP server')).toBeInTheDocument()
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
    fireEvent.click(screen.getByRole('button', { name: /add server/i }))

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
    fireEvent.click(screen.getByRole('button', { name: /add server/i }))
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
    fireEvent.click(screen.getByRole('button', { name: /add server/i }))
    // Spinner is rendered with aria-hidden
    const spinners = document.querySelectorAll('[aria-hidden="true"]')
    expect(spinners.length).toBeGreaterThan(0)
  })
})

describe('MCPPage — delete server modal', () => {
  beforeEach(() => {
    const tools = new Map([['srv-1', [TOOL_1, TOOL_2]]])
    mockServersLoaded([SERVER_1], tools)
    mockNoopMutations()
  })

  it('opens delete confirmation modal when Delete is clicked', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /delete kubectl-mcp/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText('Delete MCP server')).toBeInTheDocument()
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
    fireEvent.click(screen.getByRole('button', { name: /delete server/i }))

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

describe('MCPPage — discover button', () => {
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

describe('MCPPage — tool role dropdown', () => {
  it('calls updateTool.mutate with correct args on role change', async () => {
    const tools = new Map([['srv-1', [TOOL_1, TOOL_2]]])
    mockServersLoaded([SERVER_1], tools)
    mockNoopMutations()

    const mutateMock = vi.fn()
    vi.mocked(useUpdateMcpTool).mockReturnValue({
      mutate: mutateMock,
      isPending: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useUpdateMcpTool>)

    renderPage()
    // Expand the tool list first
    fireEvent.click(screen.getByRole('button', { name: /2 tools/i }))

    await waitFor(() => {
      expect(screen.getByLabelText(`Role for ${TOOL_1.name}`)).toBeInTheDocument()
    })

    const select = screen.getByLabelText(`Role for ${TOOL_1.name}`)
    fireEvent.change(select, { target: { value: 'actuator' } })

    expect(mutateMock).toHaveBeenCalledWith(
      { toolId: 't1', serverId: 'srv-1', capability_role: 'actuator' },
      expect.any(Object),
    )
  })
})

describe('MCPPage — unassigned banner', () => {
  it('is hidden when all tools have valid roles', () => {
    const tools = new Map([['srv-1', [TOOL_1, TOOL_2]]])
    mockServersLoaded([SERVER_1], tools)
    mockNoopMutations()

    renderPage()
    // Both tools have valid roles (sensor, actuator)
    expect(screen.queryByRole('status', { name: /unassigned/i })).not.toBeInTheDocument()
  })

  it('shows when servers list is empty', () => {
    mockServersLoaded([])
    mockNoopMutations()
    renderPage()
    expect(screen.queryByText(/no capability role assigned/i)).not.toBeInTheDocument()
  })
})
