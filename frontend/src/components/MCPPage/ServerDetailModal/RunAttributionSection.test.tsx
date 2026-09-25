import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import { RunAttributionSection } from './RunAttributionSection'
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

describe('RunAttributionSection', () => {
  it('shows "Off" by default (no run_attribution field)', () => {
    render(<RunAttributionSection server={baseServer} />)
    expect(screen.getByText('Off')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /turn off/i })).not.toBeInTheDocument()
  })

  it('shows the Relay preset names', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      run_attribution: {
        mode: 'relay',
        on_behalf_of_header: 'X-Relay-On-Behalf-Of',
        session_ref_header: 'X-Relay-Session-Ref',
        traceparent_header: 'traceparent',
      },
    }
    render(<RunAttributionSection server={server} />)
    expect(
      screen.getByText('Relay preset: X-Relay-On-Behalf-Of, X-Relay-Session-Ref, traceparent'),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /turn off/i })).toBeInTheDocument()
  })

  it('Edit, choose Custom, fill names, Save mutates with {mode: custom, ...}', () => {
    render(<RunAttributionSection server={baseServer} />)

    fireEvent.click(screen.getByRole('button', { name: /^edit$/i }))
    fireEvent.change(screen.getByLabelText(/run attribution mode/i), { target: { value: 'custom' } })
    fireEvent.change(screen.getByPlaceholderText('e.g. X-Actor'), { target: { value: 'X-Actor' } })
    fireEvent.click(screen.getByRole('button', { name: /^save$/i }))

    expect(mockUpdateMutate).toHaveBeenCalledWith(
      {
        id: 'srv-1',
        name: 'test-server',
        url: 'https://mcp-test-server:8443/mcp',
        run_attribution: { mode: 'custom', on_behalf_of_header: 'X-Actor' },
      },
      expect.anything(),
    )
  })

  it('"Turn off" mutates {mode: off}', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      run_attribution: {
        mode: 'relay',
        on_behalf_of_header: 'X-Relay-On-Behalf-Of',
        session_ref_header: 'X-Relay-Session-Ref',
        traceparent_header: 'traceparent',
      },
    }
    render(<RunAttributionSection server={server} />)

    fireEvent.click(screen.getByRole('button', { name: /turn off/i }))

    expect(mockUpdateMutate).toHaveBeenCalledWith(
      {
        id: 'srv-1',
        name: 'test-server',
        url: 'https://mcp-test-server:8443/mcp',
        run_attribution: { mode: 'off' },
      },
      expect.anything(),
    )
  })

  it('blocks save when custom has no header name', () => {
    render(<RunAttributionSection server={baseServer} />)

    fireEvent.click(screen.getByRole('button', { name: /^edit$/i }))
    fireEvent.change(screen.getByLabelText(/run attribution mode/i), { target: { value: 'custom' } })
    fireEvent.click(screen.getByRole('button', { name: /^save$/i }))

    expect(mockUpdateMutate).not.toHaveBeenCalled()
    expect(screen.getByText(/enter at least one header name/i)).toBeInTheDocument()
  })

  it('renders the error detail when the save mutation fails', () => {
    mockUpdateMutate = vi.fn((_vars, opts) => {
      opts.onError({ message: 'update failed', detail: 'invalid run_attribution' })
    })
    render(<RunAttributionSection server={baseServer} />)

    fireEvent.click(screen.getByRole('button', { name: /^edit$/i }))
    fireEvent.change(screen.getByLabelText(/run attribution mode/i), { target: { value: 'relay' } })
    act(() => {
      fireEvent.click(screen.getByRole('button', { name: /^save$/i }))
    })

    expect(screen.getByText('invalid run_attribution')).toBeInTheDocument()
  })

  it('hides the edit affordance for a managed server', () => {
    const server: ApiMcpServer = { ...baseServer, trust_tier: 'managed' as const }
    render(<RunAttributionSection server={server} />)
    expect(screen.queryByRole('button', { name: /^edit$/i })).not.toBeInTheDocument()
  })
})
