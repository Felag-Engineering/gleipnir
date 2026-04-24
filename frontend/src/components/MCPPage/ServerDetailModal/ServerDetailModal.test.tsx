import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { ServerDetailModal } from './ServerDetailModal'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'
import { MASKED_HEADER_VALUE } from '@/api/types'

// Shared mutate spy — reset between tests that need to inspect calls.
let mockMutate = vi.fn()

// Mock useUpdateMcpServer so tests don't need a QueryClientProvider.
vi.mock('@/hooks/mutations/servers', () => ({
  useUpdateMcpServer: () => ({
    mutate: mockMutate,
    isPending: false,
    error: null,
    reset: vi.fn(),
  }),
}))

const server: ApiMcpServer = {
  id: 'srv-1',
  name: 'test-server',
  url: 'http://mcp-test-server:8090/mcp',
  last_discovered_at: '2026-04-03T15:44:01Z',
  has_drift: false,
  created_at: '2026-04-03T15:43:55Z',
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
  },
  {
    id: 't2',
    server_id: 'srv-1',
    name: 'get_time',
    description: 'Get current time.',
    input_schema: { properties: {}, type: 'object' },
  },
]

const defaultProps = {
  server,
  tools,
  toolsLoading: false,
  isDiscovering: false,
  policies: undefined,
  onClose: vi.fn(),
  onDiscover: vi.fn(),
  onDelete: vi.fn(),
}

describe('ServerDetailModal', () => {
  it('renders server name and tool count', () => {
    render(<ServerDetailModal {...defaultProps} />)
    expect(screen.getByText('test-server')).toBeInTheDocument()
    expect(screen.getByText('2 tools')).toBeInTheDocument()
  })

  it('renders server URL', () => {
    render(<ServerDetailModal {...defaultProps} />)
    expect(screen.getByText(/mcp-test-server:8090/)).toBeInTheDocument()
  })

  it('renders all tool names in accordion', () => {
    render(<ServerDetailModal {...defaultProps} />)
    expect(screen.getByText('echo')).toBeInTheDocument()
    expect(screen.getByText('get_time')).toBeInTheDocument()
  })

  it('calls onClose when close button is clicked', () => {
    const onClose = vi.fn()
    render(<ServerDetailModal {...defaultProps} onClose={onClose} />)
    fireEvent.click(screen.getByLabelText('Close'))
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('calls onDiscover when Rediscover is clicked', () => {
    const onDiscover = vi.fn()
    render(<ServerDetailModal {...defaultProps} onDiscover={onDiscover} />)
    fireEvent.click(screen.getByRole('button', { name: /rediscover/i }))
    expect(onDiscover).toHaveBeenCalledWith('srv-1')
  })

  it('calls onDelete when Delete is clicked', () => {
    const onDelete = vi.fn()
    render(<ServerDetailModal {...defaultProps} onDelete={onDelete} />)
    fireEvent.click(screen.getByRole('button', { name: /delete/i }))
    expect(onDelete).toHaveBeenCalledWith(server, 2)
  })

  it('shows Discovering text when isDiscovering is true', () => {
    render(<ServerDetailModal {...defaultProps} isDiscovering={true} />)
    expect(screen.getByText(/discovering/i)).toBeInTheDocument()
  })

  it('shows Drift badge when server has_drift is true', () => {
    const driftServer = { ...server, has_drift: true }
    render(<ServerDetailModal {...defaultProps} server={driftServer} />)
    expect(screen.getByText('Drift')).toBeInTheDocument()
  })

  it('shows Unreachable badge when last_discovered_at is null', () => {
    const unreachableServer = { ...server, last_discovered_at: null }
    render(<ServerDetailModal {...defaultProps} server={unreachableServer} />)
    expect(screen.getByText('Unreachable')).toBeInTheDocument()
  })

  it('shows no status badge for healthy connected server', () => {
    render(<ServerDetailModal {...defaultProps} />)
    expect(screen.queryByText('Drift')).not.toBeInTheDocument()
    expect(screen.queryByText('Unreachable')).not.toBeInTheDocument()
  })

  it('shows loading skeletons when toolsLoading is true', () => {
    render(<ServerDetailModal {...defaultProps} toolsLoading={true} tools={undefined} />)
    const skeletons = document.querySelectorAll('[aria-hidden="true"]')
    expect(skeletons.length).toBeGreaterThan(0)
  })

  it('closes on Escape key', () => {
    const onClose = vi.fn()
    render(<ServerDetailModal {...defaultProps} onClose={onClose} />)
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledOnce()
  })
})

describe('ServerDetailModal — auth header editor', () => {
  it('shows "Auth headers" label when server has no auth_header_keys', () => {
    render(<ServerDetailModal {...defaultProps} />)
    expect(screen.getByRole('button', { name: 'Auth headers' })).toBeInTheDocument()
  })

  it('shows "Auth (N)" label when server has auth_header_keys populated', () => {
    const serverWithKeys: ApiMcpServer = { ...server, auth_header_keys: ['x-api-key', 'x-token'] }
    render(<ServerDetailModal {...defaultProps} server={serverWithKeys} />)
    expect(screen.getByRole('button', { name: 'Auth (2)' })).toBeInTheDocument()
  })

  it('opening the editor seeds one row per key with MASKED_HEADER_VALUE in the value field', () => {
    const serverWithKeys: ApiMcpServer = { ...server, auth_header_keys: ['x-api-key', 'x-token'] }
    render(<ServerDetailModal {...defaultProps} server={serverWithKeys} />)

    fireEvent.click(screen.getByRole('button', { name: 'Auth (2)' }))

    const keyInputs = screen.getAllByRole('textbox', { name: /header name/i })
    const valueInputs = screen.getAllByRole('textbox', { name: /header value/i })

    expect(keyInputs).toHaveLength(2)
    expect(valueInputs).toHaveLength(2)
    expect(keyInputs[0]).toHaveValue('x-api-key')
    expect(keyInputs[1]).toHaveValue('x-token')
    expect(valueInputs[0]).toHaveValue(MASKED_HEADER_VALUE)
    expect(valueInputs[1]).toHaveValue(MASKED_HEADER_VALUE)
  })

  it('Save calls mutate with sentinel preserved for untouched rows and plaintext for edited rows', () => {
    mockMutate = vi.fn()
    const serverWithKeys: ApiMcpServer = { ...server, auth_header_keys: ['x-api-key', 'x-token'] }
    render(<ServerDetailModal {...defaultProps} server={serverWithKeys} />)

    fireEvent.click(screen.getByRole('button', { name: 'Auth (2)' }))

    // Change only the second header's value; leave the first as-is (sentinel).
    const valueInputs = screen.getAllByRole('textbox', { name: /header value/i })
    fireEvent.change(valueInputs[1], { target: { value: 'new-secret' } })

    fireEvent.click(screen.getByRole('button', { name: /^save$/i }))

    expect(mockMutate).toHaveBeenCalledOnce()
    expect(mockMutate).toHaveBeenCalledWith(
      {
        id: 'srv-1',
        name: 'test-server',
        url: 'http://mcp-test-server:8090/mcp',
        auth_headers: [
          { key: 'x-api-key', value: MASKED_HEADER_VALUE },
          { key: 'x-token', value: 'new-secret' },
        ],
      },
      expect.any(Object),
    )
  })
})

describe('ServerDetailModal — accessibility', () => {
  it("has role='dialog' and aria-modal='true' on the content box", () => {
    render(<ServerDetailModal {...defaultProps} />)
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveAttribute('aria-modal', 'true')
  })

  it('dialog has accessible name via aria-label', () => {
    render(<ServerDetailModal {...defaultProps} />)
    const dialog = screen.getByRole('dialog')
    expect(dialog.getAttribute('aria-label')).toBe('test-server details')
  })

  it('wraps content in FocusTrap (all interactive elements inside dialog)', () => {
    render(<ServerDetailModal {...defaultProps} />)
    const dialog = screen.getByRole('dialog')
    const closeBtn = screen.getByRole('button', { name: 'Close' })
    const rediscoverBtn = screen.getByRole('button', { name: /rediscover/i })
    const deleteBtn = screen.getByRole('button', { name: /delete/i })
    expect(dialog.contains(closeBtn)).toBe(true)
    expect(dialog.contains(rediscoverBtn)).toBe(true)
    expect(dialog.contains(deleteBtn)).toBe(true)
  })
})
