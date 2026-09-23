import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import { CaCertificateSection } from './CaCertificateSection'
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

describe('CaCertificateSection', () => {
  it('renders subject, fingerprint, and expiry for a pinned CA', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      ca_cert_pem: '-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----',
      ca_certificates: [
        {
          subject: 'CN=gleipnir-test-ca',
          sha256_fingerprint: 'deadbeef'.repeat(8),
          not_after: '2099-01-01T00:00:00Z',
        },
      ],
    }
    render(<CaCertificateSection server={server} />)

    expect(screen.getByText('CN=gleipnir-test-ca')).toBeInTheDocument()
    expect(screen.getByText('deadbeef'.repeat(8))).toBeInTheDocument()
    expect(screen.getByText(/Expires/)).toBeInTheDocument()
    expect(screen.queryByText('Expired')).not.toBeInTheDocument()
  })

  it('shows an Expired badge for a past not_after', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      ca_cert_pem: 'fake-pem',
      ca_certificates: [
        {
          subject: 'CN=expired-ca',
          sha256_fingerprint: 'aa'.repeat(32),
          not_after: '2020-01-01T00:00:00Z',
        },
      ],
    }
    render(<CaCertificateSection server={server} />)

    expect(screen.getByText('Expired')).toBeInTheDocument()
  })

  it('shows a no-CA message when no certificate is pinned', () => {
    render(<CaCertificateSection server={baseServer} />)
    expect(screen.getByText(/No CA certificate pinned/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /add ca certificate/i })).toBeInTheDocument()
  })

  it('Save sends the entered PEM', () => {
    render(<CaCertificateSection server={baseServer} />)

    fireEvent.click(screen.getByRole('button', { name: /add ca certificate/i }))
    fireEvent.change(screen.getByLabelText(/ca certificate pem/i), {
      target: { value: '-----BEGIN CERTIFICATE-----\nnew\n-----END CERTIFICATE-----' },
    })
    fireEvent.click(screen.getByRole('button', { name: /^save$/i }))

    expect(mockUpdateMutate).toHaveBeenCalledWith(
      {
        id: 'srv-1',
        name: 'test-server',
        url: 'https://mcp-test-server:8443/mcp',
        ca_cert_pem: '-----BEGIN CERTIFICATE-----\nnew\n-----END CERTIFICATE-----',
      },
      expect.anything(),
    )
  })

  it('Remove CA sends an empty string', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      ca_cert_pem: 'fake-pem',
      ca_certificates: [
        { subject: 'CN=ca', sha256_fingerprint: 'bb'.repeat(32), not_after: '2099-01-01T00:00:00Z' },
      ],
    }
    render(<CaCertificateSection server={server} />)

    fireEvent.click(screen.getByRole('button', { name: /remove ca/i }))

    expect(mockUpdateMutate).toHaveBeenCalledWith(
      { id: 'srv-1', name: 'test-server', url: 'https://mcp-test-server:8443/mcp', ca_cert_pem: '' },
      expect.anything(),
    )
  })

  it('shows the error detail when the save mutation fails', () => {
    mockUpdateMutate = vi.fn((_vars, opts) => {
      opts.onError({ message: 'update failed', detail: 'invalid ca_cert_pem' })
    })
    render(<CaCertificateSection server={baseServer} />)

    fireEvent.click(screen.getByRole('button', { name: /add ca certificate/i }))
    fireEvent.change(screen.getByLabelText(/ca certificate pem/i), {
      target: { value: 'not a cert' },
    })
    act(() => {
      fireEvent.click(screen.getByRole('button', { name: /^save$/i }))
    })

    expect(screen.getByText('invalid ca_cert_pem')).toBeInTheDocument()
  })

  it('hides the edit affordance for a managed server', () => {
    const server: ApiMcpServer = { ...baseServer, trust_tier: 'managed' as const }
    render(<CaCertificateSection server={server} />)
    expect(screen.queryByRole('button', { name: /add ca certificate/i })).not.toBeInTheDocument()
  })
})
