import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ApiMcpServer, ApiMcpTool } from '@/api/types'
import { ArcadeAuthSection } from './ArcadeAuthSection'

// --- Mocks ---

let mockAuthorizeMutateAsync = vi.fn()
let mockWaitMutateAsync = vi.fn()

vi.mock('@/hooks/mutations/arcade', () => ({
  useArcadeAuthorizeToolkit: () => ({
    mutateAsync: mockAuthorizeMutateAsync,
    isPending: false,
  }),
  useArcadeAuthorizeWait: () => ({
    mutateAsync: mockWaitMutateAsync,
    isPending: false,
  }),
}))

// --- Fixtures ---

const server: ApiMcpServer = {
  id: 'srv-1',
  name: 'arcade-server',
  url: 'https://api.arcade.dev/mcp/test',
  last_discovered_at: '2026-04-01T00:00:00Z',
  has_drift: false,
  created_at: '2026-04-01T00:00:00Z',
  is_arcade_gateway: true,
  auth_header_keys: ['Authorization', 'Arcade-User-ID'],
}

const tools: ApiMcpTool[] = [
  {
    id: 't1',
    server_id: 'srv-1',
    name: 'Gmail_SendEmail',
    description: 'Send email',
    input_schema: {},
    enabled: true,
  },
  {
    id: 't2',
    server_id: 'srv-1',
    name: 'Gmail_ListEmails',
    description: 'List emails',
    input_schema: {},
    enabled: true,
  },
  {
    id: 't3',
    server_id: 'srv-1',
    name: 'GoogleCalendar_CreateEvent',
    description: 'Create calendar event',
    input_schema: {},
    enabled: true,
  },
]

function renderWithClient(ui: React.ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>)
}

// --- Tests ---

describe('ArcadeAuthSection', () => {
  beforeEach(() => {
    mockAuthorizeMutateAsync = vi.fn()
    mockWaitMutateAsync = vi.fn()
  })

  it('renders one row per toolkit derived from tools', () => {
    renderWithClient(<ArcadeAuthSection server={server} tools={tools} canManage={true} />)
    expect(screen.getByText('Gmail')).toBeInTheDocument()
    expect(screen.getByText('GoogleCalendar')).toBeInTheDocument()
    expect(screen.getByText('(2 tools)')).toBeInTheDocument()
    expect(screen.getByText('(1 tool)')).toBeInTheDocument()
  })

  it('renders no rows when tools array is empty', () => {
    const { container } = renderWithClient(
      <ArcadeAuthSection server={server} tools={[]} canManage={true} />,
    )
    expect(container.firstChild).toBeNull()
  })

  it('canManage=false renders no buttons', () => {
    renderWithClient(<ArcadeAuthSection server={server} tools={tools} canManage={false} />)
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('shows Check → button initially (unknown state)', () => {
    renderWithClient(<ArcadeAuthSection server={server} tools={tools} canManage={true} />)
    const buttons = screen.getAllByRole('button', { name: /check/i })
    expect(buttons.length).toBeGreaterThan(0)
  })

  it('clicking Authorize with completed response flips badge to authorized', async () => {
    mockAuthorizeMutateAsync.mockResolvedValueOnce({ status: 'completed' })

    renderWithClient(<ArcadeAuthSection server={server} tools={tools} canManage={true} />)

    const buttons = screen.getAllByRole('button', { name: /check/i })
    await act(async () => {
      fireEvent.click(buttons[0])
    })

    await waitFor(() => {
      expect(screen.getByText('✓ Authorized')).toBeInTheDocument()
    })
  })

  it('clicking Authorize with pending response opens URL and waits, then shows authorized', async () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null)

    mockAuthorizeMutateAsync.mockResolvedValueOnce({
      status: 'pending',
      url: 'https://arcade.dev/oauth',
      auth_id: 'auth-123',
    })
    mockWaitMutateAsync.mockResolvedValueOnce({ status: 'completed' })

    renderWithClient(<ArcadeAuthSection server={server} tools={tools} canManage={true} />)

    const buttons = screen.getAllByRole('button', { name: /check/i })
    await act(async () => {
      fireEvent.click(buttons[0])
    })

    await waitFor(() => {
      expect(openSpy).toHaveBeenCalledWith('https://arcade.dev/oauth', '_blank', 'noopener')
    })

    await waitFor(() => {
      expect(screen.getByText('✓ Authorized')).toBeInTheDocument()
    })

    openSpy.mockRestore()
  })

  it('pending then completed: wait loop re-issues and final state is authorized', async () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null)

    mockAuthorizeMutateAsync.mockResolvedValueOnce({
      status: 'pending',
      url: 'https://arcade.dev/oauth',
      auth_id: 'auth-123',
    })
    // First wait returns pending; second returns completed.
    mockWaitMutateAsync
      .mockResolvedValueOnce({ status: 'pending', auth_id: 'auth-123', url: 'https://arcade.dev/oauth' })
      .mockResolvedValueOnce({ status: 'completed' })

    renderWithClient(<ArcadeAuthSection server={server} tools={tools} canManage={true} />)

    const buttons = screen.getAllByRole('button', { name: /check/i })
    await act(async () => {
      fireEvent.click(buttons[0])
    })

    await waitFor(() => {
      expect(mockWaitMutateAsync).toHaveBeenCalledTimes(2)
    })
    await waitFor(() => {
      expect(screen.getByText('✓ Authorized')).toBeInTheDocument()
    })

    openSpy.mockRestore()
  })

  it('wait loop opens a new popup when a fresh pending grant is returned with a new auth_id', async () => {
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null)

    // Initial Authorize: first tool needs OAuth (auth_id A1, url U1).
    mockAuthorizeMutateAsync.mockResolvedValueOnce({
      status: 'pending',
      url: 'https://arcade.dev/oauth/A1',
      auth_id: 'A1',
    })
    // First /wait poll: A1 still pending (user hasn't clicked yet).
    // Second /wait poll: A1 completed, backend re-walked the toolkit and
    //   surfaced a new pending grant (A2, U2) for the next tool.
    // Third /wait poll: A2 still pending.
    // Fourth /wait poll: completed.
    mockWaitMutateAsync
      .mockResolvedValueOnce({ status: 'pending', auth_id: 'A1', url: 'https://arcade.dev/oauth/A1' })
      .mockResolvedValueOnce({ status: 'pending', auth_id: 'A2', url: 'https://arcade.dev/oauth/A2' })
      .mockResolvedValueOnce({ status: 'pending', auth_id: 'A2', url: 'https://arcade.dev/oauth/A2' })
      .mockResolvedValueOnce({ status: 'completed' })

    renderWithClient(<ArcadeAuthSection server={server} tools={tools} canManage={true} />)

    const buttons = screen.getAllByRole('button', { name: /check/i })
    await act(async () => {
      fireEvent.click(buttons[0])
    })

    // U1 popup is opened on the initial Authorize response, U2 popup is opened
    // when the wait loop sees a new auth_id.
    await waitFor(() => {
      expect(openSpy).toHaveBeenCalledWith('https://arcade.dev/oauth/A1', '_blank', 'noopener')
      expect(openSpy).toHaveBeenCalledWith('https://arcade.dev/oauth/A2', '_blank', 'noopener')
    })

    // Subsequent polls use the new auth_id (A2), not the original (A1).
    await waitFor(() => {
      expect(mockWaitMutateAsync).toHaveBeenLastCalledWith({ toolkit: 'Gmail', auth_id: 'A2' })
    })

    await waitFor(() => {
      expect(screen.getByText('✓ Authorized')).toBeInTheDocument()
    })

    openSpy.mockRestore()
  })

  it('concurrent click guard: while first toolkit is in-flight, second button is disabled', async () => {
    // Make the first authorize call block indefinitely so we can inspect in-flight state.
    let resolveFirst: (val: unknown) => void
    const firstCallPromise = new Promise((resolve) => {
      resolveFirst = resolve
    })
    mockAuthorizeMutateAsync.mockReturnValueOnce(firstCallPromise)

    renderWithClient(<ArcadeAuthSection server={server} tools={tools} canManage={true} />)

    const buttons = screen.getAllByRole('button', { name: /check/i })
    expect(buttons.length).toBeGreaterThanOrEqual(2)

    // Click the first button.
    fireEvent.click(buttons[0])

    // While in-flight, the second button should be disabled.
    await waitFor(() => {
      const allButtons = screen.queryAllByRole('button', { name: /check/i })
      // The in-flight toolkit's button is replaced by a spinner, so we check the remaining buttons.
      for (const btn of allButtons) {
        expect(btn).toBeDisabled()
      }
    })

    // Clean up: resolve the first call.
    act(() => {
      resolveFirst!({ status: 'completed' })
    })
  })
})
