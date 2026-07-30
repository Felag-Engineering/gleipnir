import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import AdminSystemPage from './AdminSystemPage'

// --- Module mocks ---

vi.mock('@/hooks/queries/admin')
vi.mock('@/hooks/mutations/admin')

import { useAdminSettings, useSystemInfo } from '@/hooks/queries/admin'
import { useUpdateAdminSettings } from '@/hooks/mutations/admin'

// --- Helpers ---

type Status = 'pending' | 'error' | 'success'

function query<T>(status: Status, data: T, refetch = vi.fn()) {
  return { data, status, refetch } as unknown
}

function mockAll(status: Status, refetch = vi.fn()) {
  vi.mocked(useAdminSettings).mockReturnValue(
    query(
      status,
      status === 'success' ? { public_url: '', max_tokens_per_run: '', max_tool_calls_per_run: '' } : undefined,
      refetch,
    ) as ReturnType<typeof useAdminSettings>,
  )
  vi.mocked(useSystemInfo).mockReturnValue(
    query(
      status,
      status === 'success'
        ? {
            version: '1.1.1',
            uptime: '1h',
            db_size: '1 MB',
            mcp_servers: 0,
            policies: 0,
            users: 1,
          }
        : undefined,
      refetch,
    ) as ReturnType<typeof useSystemInfo>,
  )
  vi.mocked(useUpdateAdminSettings).mockReturnValue({
    mutate: vi.fn(),
    isPending: false,
    isError: false,
  } as unknown as ReturnType<typeof useUpdateAdminSettings>)
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <AdminSystemPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

// --- Tests ---

describe('AdminSystemPage — loading state', () => {
  beforeEach(() => mockAll('pending'))

  it('renders skeleton blocks while queries load', () => {
    const { container } = renderPage()
    expect(container.querySelectorAll('[aria-hidden="true"]').length).toBeGreaterThan(0)
  })
})

describe('AdminSystemPage — error state', () => {
  it('shows a recoverable error with retry for each section on failure', () => {
    mockAll('error')
    renderPage()
    expect(screen.getAllByRole('alert').length).toBeGreaterThan(0)
    expect(screen.getAllByRole('button', { name: /retry/i }).length).toBeGreaterThan(0)
    expect(screen.getByText(/failed to load system information/i)).toBeInTheDocument()
  })

  it('retry button triggers a refetch', () => {
    const refetch = vi.fn()
    mockAll('error', refetch)
    renderPage()
    fireEvent.click(screen.getAllByRole('button', { name: /retry/i })[0])
    expect(refetch).toHaveBeenCalled()
  })
})

describe('AdminSystemPage — success state', () => {
  beforeEach(() => mockAll('success'))

  it('renders the system info tiles without skeletons or error alerts', () => {
    renderPage()
    expect(screen.getByText('Version')).toBeInTheDocument()
    expect(screen.getByText('1.1.1')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /retry/i })).not.toBeInTheDocument()
  })
})
