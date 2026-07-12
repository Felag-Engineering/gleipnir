import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import AdminModelsPage from './AdminModelsPage'

// --- Module mocks ---

vi.mock('@/hooks/queries/admin')
vi.mock('@/hooks/queries/users')
vi.mock('@/hooks/queries/openaiCompatProviders')
vi.mock('@/hooks/mutations/admin')
// The OpenAI-compat section pulls in its own mutation hooks; stub it out so this
// suite only exercises the sections under test.
vi.mock('@/components/admin/OpenAICompatProvidersSection', () => ({
  OpenAICompatProvidersSection: () => <div data-testid="compat-section" />,
}))

import { useProviders, useAllAdminModels, useAdminSettings } from '@/hooks/queries/admin'
import { useModels } from '@/hooks/queries/users'
import { useOpenAICompatProviders } from '@/hooks/queries/openaiCompatProviders'
import {
  useSetProviderKey,
  useUpdateAdminSettings,
  useSetModelEnabled,
} from '@/hooks/mutations/admin'

// --- Helpers ---

type Status = 'pending' | 'error' | 'success'

function query<T>(status: Status, data: T, refetch = vi.fn()) {
  return { data, status, refetch } as unknown
}

function mutation() {
  return { mutate: vi.fn(), isPending: false } as unknown
}

function mockAll(status: Status, refetch = vi.fn()) {
  vi.mocked(useProviders).mockReturnValue(
    query(status, status === 'success' ? [{ name: 'anthropic', has_key: true, masked_key: 'sk-…abc' }] : undefined, refetch) as ReturnType<typeof useProviders>,
  )
  vi.mocked(useAllAdminModels).mockReturnValue(
    query(status, status === 'success' ? [] : undefined, refetch) as ReturnType<typeof useAllAdminModels>,
  )
  vi.mocked(useAdminSettings).mockReturnValue(
    query(status, status === 'success' ? { default_model: '' } : undefined, refetch) as ReturnType<typeof useAdminSettings>,
  )
  vi.mocked(useModels).mockReturnValue(
    query(status, status === 'success' ? [] : undefined, refetch) as ReturnType<typeof useModels>,
  )
  vi.mocked(useOpenAICompatProviders).mockReturnValue(
    query(status, status === 'success' ? [] : undefined, refetch) as ReturnType<typeof useOpenAICompatProviders>,
  )
  vi.mocked(useSetProviderKey).mockReturnValue(mutation() as ReturnType<typeof useSetProviderKey>)
  vi.mocked(useUpdateAdminSettings).mockReturnValue(mutation() as ReturnType<typeof useUpdateAdminSettings>)
  vi.mocked(useSetModelEnabled).mockReturnValue(mutation() as ReturnType<typeof useSetModelEnabled>)
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <AdminModelsPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

// --- Tests ---

describe('AdminModelsPage — loading state', () => {
  beforeEach(() => mockAll('pending'))

  it('renders skeleton blocks while queries load', () => {
    const { container } = renderPage()
    expect(container.querySelectorAll('[aria-hidden="true"]').length).toBeGreaterThan(0)
  })
})

describe('AdminModelsPage — error state', () => {
  it('shows a recoverable error with retry for each data section on failure', () => {
    mockAll('error')
    renderPage()
    // Every wrapped section renders its own alert + Retry button.
    expect(screen.getAllByRole('alert').length).toBeGreaterThan(0)
    expect(screen.getAllByRole('button', { name: /retry/i }).length).toBeGreaterThan(0)
    expect(screen.getByText(/failed to load providers/i)).toBeInTheDocument()
  })

  it('retry button triggers a refetch', () => {
    const refetch = vi.fn()
    mockAll('error', refetch)
    renderPage()
    fireEvent.click(screen.getAllByRole('button', { name: /retry/i })[0])
    expect(refetch).toHaveBeenCalled()
  })
})

describe('AdminModelsPage — success state', () => {
  beforeEach(() => mockAll('success'))

  it('renders section content without skeletons or error alerts', () => {
    renderPage()
    expect(screen.getByText('API Keys')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /retry/i })).not.toBeInTheDocument()
  })
})
