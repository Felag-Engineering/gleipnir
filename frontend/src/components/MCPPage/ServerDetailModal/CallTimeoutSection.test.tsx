import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import { CallTimeoutSection } from './CallTimeoutSection'
import type { ApiMcpServer } from '@/api/types'

let mockUpdateMutate = vi.fn()
let mockIsPending = false

vi.mock('@/hooks/mutations/servers', () => ({
  useUpdateMcpServer: () => ({
    mutate: mockUpdateMutate,
    isPending: mockIsPending,
  }),
}))

const baseServer: ApiMcpServer = {
  id: 'srv-1',
  name: 'test-server',
  url: 'https://mcp-test-server:8443/mcp',
  last_discovered_at: '2026-04-03T15:44:01Z',
  has_drift: false,
  created_at: '2026-04-03T15:43:55Z',
  is_arcade_gateway: false,
  trust_tier: 'external' as const,
  plugin_instance_id: null,
  editable: true,
  protocol_version: '2026-07-28',
}

beforeEach(() => {
  mockUpdateMutate = vi.fn()
  mockIsPending = false
})

describe('CallTimeoutSection', () => {
  it('shows "Default (Ns)" when unset', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      call_timeout_seconds: null,
      effective_call_timeout_seconds: 30,
    }
    render(<CallTimeoutSection server={server} />)
    expect(screen.getByText('Default (30s)')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /set override/i })).toBeInTheDocument()
  })

  it('shows the override in seconds when set', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      call_timeout_seconds: 120,
      effective_call_timeout_seconds: 120,
    }
    render(<CallTimeoutSection server={server} />)
    expect(screen.getByText('120s')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /^edit$/i })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /use default/i })).toBeInTheDocument()
  })

  it('Save sends the parsed value with no ca_cert_pem key', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      call_timeout_seconds: null,
      effective_call_timeout_seconds: 30,
    }
    render(<CallTimeoutSection server={server} />)

    fireEvent.click(screen.getByRole('button', { name: /set override/i }))
    fireEvent.change(screen.getByLabelText(/call timeout seconds/i), { target: { value: '300' } })
    fireEvent.click(screen.getByRole('button', { name: /^save$/i }))

    expect(mockUpdateMutate).toHaveBeenCalledWith(
      { id: 'srv-1', name: 'test-server', url: 'https://mcp-test-server:8443/mcp', call_timeout_seconds: 300 },
      expect.anything(),
    )
    const [sentBody] = mockUpdateMutate.mock.calls[0]
    expect(sentBody).not.toHaveProperty('ca_cert_pem')
  })

  it('"Use default" sends 0', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      call_timeout_seconds: 120,
      effective_call_timeout_seconds: 120,
    }
    render(<CallTimeoutSection server={server} />)

    fireEvent.click(screen.getByRole('button', { name: /use default/i }))

    expect(mockUpdateMutate).toHaveBeenCalledWith(
      { id: 'srv-1', name: 'test-server', url: 'https://mcp-test-server:8443/mcp', call_timeout_seconds: 0 },
      expect.anything(),
    )
  })

  it('blocks save on an invalid input', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      call_timeout_seconds: null,
      effective_call_timeout_seconds: 30,
    }
    render(<CallTimeoutSection server={server} />)

    fireEvent.click(screen.getByRole('button', { name: /set override/i }))
    fireEvent.change(screen.getByLabelText(/call timeout seconds/i), { target: { value: '700' } })
    fireEvent.click(screen.getByRole('button', { name: /^save$/i }))

    expect(mockUpdateMutate).not.toHaveBeenCalled()
    expect(screen.getByText(/must be between 1 and 600 seconds/i)).toBeInTheDocument()
  })

  it('renders the error detail when the save mutation fails', () => {
    mockUpdateMutate = vi.fn((_vars, opts) => {
      opts.onError({ message: 'update failed', detail: 'invalid call_timeout_seconds' })
    })
    const server: ApiMcpServer = {
      ...baseServer,
      call_timeout_seconds: null,
      effective_call_timeout_seconds: 30,
    }
    render(<CallTimeoutSection server={server} />)

    fireEvent.click(screen.getByRole('button', { name: /set override/i }))
    fireEvent.change(screen.getByLabelText(/call timeout seconds/i), { target: { value: '120' } })
    act(() => {
      fireEvent.click(screen.getByRole('button', { name: /^save$/i }))
    })

    expect(screen.getByText('invalid call_timeout_seconds')).toBeInTheDocument()
  })

  it('hides the edit affordance for a managed server', () => {
    const server: ApiMcpServer = { ...baseServer, trust_tier: 'managed' as const }
    render(<CallTimeoutSection server={server} />)
    expect(screen.queryByRole('button', { name: /set override/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^edit$/i })).not.toBeInTheDocument()
  })
})
