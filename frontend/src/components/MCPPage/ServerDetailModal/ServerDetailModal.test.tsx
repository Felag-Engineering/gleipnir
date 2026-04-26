import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ServerDetailModal } from './ServerDetailModal'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'

// Shared mutation spies — reset in beforeEach where needed.
let mockUpdateMutate = vi.fn()
let mockSetHeaderMutate = vi.fn()
let mockDeleteHeaderMutate = vi.fn()
let mockSetToolEnabledMutate = vi.fn()

vi.mock('@/hooks/mutations/servers', () => ({
  useUpdateMcpServer: () => ({
    mutate: mockUpdateMutate,
    isPending: false,
    error: null,
    reset: vi.fn(),
  }),
  useSetMcpServerHeader: () => ({
    mutate: mockSetHeaderMutate,
    isPending: false,
    error: null,
  }),
  useDeleteMcpServerHeader: () => ({
    mutate: mockDeleteHeaderMutate,
    isPending: false,
    error: null,
  }),
  useSetMcpToolEnabled: () => ({
    mutate: mockSetToolEnabledMutate,
    isPending: false,
    error: null,
  }),
}))

function renderWithClient(ui: React.ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

const server: ApiMcpServer = {
  id: 'srv-1',
  name: 'test-server',
  url: 'http://mcp-test-server:8090/mcp',
  last_discovered_at: '2026-04-03T15:44:01Z',
  has_drift: false,
  created_at: '2026-04-03T15:43:55Z',
  is_arcade_gateway: false,
}

const tools: ApiMcpTool[] = [
  {
    id: 't1',
    server_id: 'srv-1',
    name: 'echo',
    description: 'Echo message back.',
    input_schema: {
      properties: { message: { type: 'string' } },
      required: ['message'],
      type: 'object',
    },
    enabled: true,
  },
  {
    id: 't2',
    server_id: 'srv-1',
    name: 'get_time',
    description: 'Get current time.',
    input_schema: { properties: {}, type: 'object' },
    enabled: true,
  },
]

const defaultProps = {
  server,
  tools,
  toolsLoading: false,
  isDiscovering: false,
  policies: undefined,
  canManage: false,
  onClose: vi.fn(),
  onDiscover: vi.fn(),
  onDelete: vi.fn(),
}

describe('ServerDetailModal', () => {
  it('renders server name and tool count', () => {
    renderWithClient(<ServerDetailModal {...defaultProps} />)
    expect(screen.getByText('test-server')).toBeInTheDocument()
    expect(screen.getByText('2 tools')).toBeInTheDocument()
  })

  it('renders server URL', () => {
    renderWithClient(<ServerDetailModal {...defaultProps} />)
    expect(screen.getByText(/mcp-test-server:8090/)).toBeInTheDocument()
  })

  it('renders all tool names in accordion', () => {
    renderWithClient(<ServerDetailModal {...defaultProps} />)
    expect(screen.getByText('echo')).toBeInTheDocument()
    expect(screen.getByText('get_time')).toBeInTheDocument()
  })

  it('calls onClose when close button is clicked', () => {
    const onClose = vi.fn()
    renderWithClient(<ServerDetailModal {...defaultProps} onClose={onClose} />)
    fireEvent.click(screen.getByLabelText('Close'))
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('calls onDiscover when Rediscover is clicked', () => {
    const onDiscover = vi.fn()
    renderWithClient(<ServerDetailModal {...defaultProps} onDiscover={onDiscover} />)
    fireEvent.click(screen.getByRole('button', { name: /rediscover/i }))
    expect(onDiscover).toHaveBeenCalledWith('srv-1')
  })

  it('calls onDelete when Delete is clicked', () => {
    const onDelete = vi.fn()
    renderWithClient(<ServerDetailModal {...defaultProps} onDelete={onDelete} />)
    fireEvent.click(screen.getByRole('button', { name: /delete/i }))
    expect(onDelete).toHaveBeenCalledWith(server, 2)
  })

  it('shows Discovering text when isDiscovering is true', () => {
    renderWithClient(<ServerDetailModal {...defaultProps} isDiscovering={true} />)
    expect(screen.getByText(/discovering/i)).toBeInTheDocument()
  })

  it('shows Drift badge when server has_drift is true', () => {
    const driftServer = { ...server, has_drift: true }
    renderWithClient(<ServerDetailModal {...defaultProps} server={driftServer} />)
    expect(screen.getByText('Drift')).toBeInTheDocument()
  })

  it('shows Unreachable badge when last_discovered_at is null', () => {
    const unreachableServer = { ...server, last_discovered_at: null }
    renderWithClient(<ServerDetailModal {...defaultProps} server={unreachableServer} />)
    expect(screen.getByText('Unreachable')).toBeInTheDocument()
  })

  it('shows no status badge for healthy connected server', () => {
    renderWithClient(<ServerDetailModal {...defaultProps} />)
    expect(screen.queryByText('Drift')).not.toBeInTheDocument()
    expect(screen.queryByText('Unreachable')).not.toBeInTheDocument()
  })

  it('shows loading skeletons when toolsLoading is true', () => {
    renderWithClient(<ServerDetailModal {...defaultProps} toolsLoading={true} tools={undefined} />)
    const skeletons = document.querySelectorAll('[aria-hidden="true"]')
    expect(skeletons.length).toBeGreaterThan(0)
  })

  it('closes on Escape key', () => {
    const onClose = vi.fn()
    renderWithClient(<ServerDetailModal {...defaultProps} onClose={onClose} />)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledOnce()
  })
})

describe('ServerDetailModal — auth header editor', () => {
  beforeEach(() => {
    mockUpdateMutate = vi.fn()
    mockSetHeaderMutate = vi.fn()
    mockDeleteHeaderMutate = vi.fn()
  })

  it('shows "Auth headers" label when server has no auth_header_keys', () => {
    render(<ServerDetailModal {...defaultProps} />)
    expect(screen.getByRole('button', { name: 'Auth headers' })).toBeInTheDocument()
  })

  it('shows "Auth (N)" label when server has auth_header_keys populated', () => {
    const serverWithKeys: ApiMcpServer = { ...server, auth_header_keys: ['x-api-key', 'x-token'] }
    render(<ServerDetailModal {...defaultProps} server={serverWithKeys} />)
    expect(screen.getByRole('button', { name: 'Auth (2)' })).toBeInTheDocument()
  })

  it('opening the editor seeds one row per key with empty value fields and disabled name inputs', () => {
    const serverWithKeys: ApiMcpServer = { ...server, auth_header_keys: ['x-api-key', 'x-token'] }
    render(<ServerDetailModal {...defaultProps} server={serverWithKeys} />)

    fireEvent.click(screen.getByRole('button', { name: 'Auth (2)' }))

    const nameInputs = screen.getAllByRole('textbox', { name: /header name/i })
    const valueInputs = screen.getAllByRole('textbox', { name: /header value/i })

    expect(nameInputs).toHaveLength(2)
    expect(valueInputs).toHaveLength(2)
    expect(nameInputs[0]).toHaveValue('x-api-key')
    expect(nameInputs[1]).toHaveValue('x-token')
    // Values are empty — no sentinel literal.
    expect(valueInputs[0]).toHaveValue('')
    expect(valueInputs[1]).toHaveValue('')
    // Existing rows have read-only name fields.
    expect(nameInputs[0]).toBeDisabled()
    expect(nameInputs[1]).toBeDisabled()
  })

  it('adding a new row and saving fires SetAuthHeader once', async () => {
    mockSetHeaderMutate = vi.fn((_args, callbacks) => {
      callbacks?.onSuccess?.()
    })

    render(<ServerDetailModal {...defaultProps} />)
    fireEvent.click(screen.getByRole('button', { name: 'Auth headers' }))
    fireEvent.click(screen.getByRole('button', { name: /\+ add header/i }))

    const nameInputs = screen.getAllByRole('textbox', { name: /header name/i })
    const valueInputs = screen.getAllByRole('textbox', { name: /header value/i })
    // The newly added row is at the end.
    fireEvent.change(nameInputs[nameInputs.length - 1], { target: { value: 'x-new-key' } })
    fireEvent.change(valueInputs[valueInputs.length - 1], { target: { value: 'new-secret' } })

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /^save$/i }))
    })

    expect(mockSetHeaderMutate).toHaveBeenCalledOnce()
    expect(mockSetHeaderMutate).toHaveBeenCalledWith(
      { id: 'srv-1', name: 'x-new-key', value: 'new-secret' },
      expect.any(Object),
    )
    expect(mockDeleteHeaderMutate).not.toHaveBeenCalled()
  })

  it('editing an existing row value and saving fires SetAuthHeader for that name', async () => {
    mockSetHeaderMutate = vi.fn((_args, callbacks) => {
      callbacks?.onSuccess?.()
    })

    const serverWithKeys: ApiMcpServer = { ...server, auth_header_keys: ['x-api-key'] }
    render(<ServerDetailModal {...defaultProps} server={serverWithKeys} />)
    fireEvent.click(screen.getByRole('button', { name: 'Auth (1)' }))

    const valueInputs = screen.getAllByRole('textbox', { name: /header value/i })
    fireEvent.change(valueInputs[0], { target: { value: 'replaced-secret' } })

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /^save$/i }))
    })

    expect(mockSetHeaderMutate).toHaveBeenCalledOnce()
    expect(mockSetHeaderMutate).toHaveBeenCalledWith(
      { id: 'srv-1', name: 'x-api-key', value: 'replaced-secret' },
      expect.any(Object),
    )
  })

  it('clicking trash on an existing row and saving fires DeleteAuthHeader once', async () => {
    mockDeleteHeaderMutate = vi.fn((_args, callbacks) => {
      callbacks?.onSuccess?.()
    })

    const serverWithKeys: ApiMcpServer = { ...server, auth_header_keys: ['x-api-key'] }
    render(<ServerDetailModal {...defaultProps} server={serverWithKeys} />)
    fireEvent.click(screen.getByRole('button', { name: 'Auth (1)' }))

    // Remove the row.
    fireEvent.click(screen.getByRole('button', { name: /remove header 1/i }))

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /^save$/i }))
    })

    expect(mockDeleteHeaderMutate).toHaveBeenCalledOnce()
    expect(mockDeleteHeaderMutate).toHaveBeenCalledWith(
      { id: 'srv-1', name: 'x-api-key' },
      expect.any(Object),
    )
    expect(mockSetHeaderMutate).not.toHaveBeenCalled()
  })

  it('saving with no changes fires no mutations', async () => {
    const serverWithKeys: ApiMcpServer = { ...server, auth_header_keys: ['x-api-key'] }
    render(<ServerDetailModal {...defaultProps} server={serverWithKeys} />)
    fireEvent.click(screen.getByRole('button', { name: 'Auth (1)' }))

    // Do not modify any row.
    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /^save$/i }))
    })

    expect(mockSetHeaderMutate).not.toHaveBeenCalled()
    expect(mockDeleteHeaderMutate).not.toHaveBeenCalled()
    expect(mockUpdateMutate).not.toHaveBeenCalled()
  })
})

describe('ServerDetailModal — accessibility', () => {
  it("has role='dialog' and aria-modal='true' on the content box", () => {
    renderWithClient(<ServerDetailModal {...defaultProps} />)
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveAttribute('aria-modal', 'true')
  })

  it('dialog has accessible name via aria-label', () => {
    renderWithClient(<ServerDetailModal {...defaultProps} />)
    const dialog = screen.getByRole('dialog')
    expect(dialog.getAttribute('aria-label')).toBe('test-server details')
  })

  it('wraps content in FocusTrap (all interactive elements inside dialog)', () => {
    renderWithClient(<ServerDetailModal {...defaultProps} />)
    const dialog = screen.getByRole('dialog')
    const closeBtn = screen.getByRole('button', { name: 'Close' })
    const rediscoverBtn = screen.getByRole('button', { name: /rediscover/i })
    const deleteBtn = screen.getByRole('button', { name: /delete/i })
    expect(dialog.contains(closeBtn)).toBe(true)
    expect(dialog.contains(rediscoverBtn)).toBe(true)
    expect(dialog.contains(deleteBtn)).toBe(true)
  })
})
