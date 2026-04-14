import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

vi.mock('../../hooks/useSSE', () => ({
  useSSE: vi.fn(() => ({ connectionState: 'connected' })),
}))

vi.mock('../../hooks/queries/users', () => ({
  useCurrentUser: vi.fn(() => ({ data: { id: '1', username: 'alice', roles: ['admin'] } })),
}))

vi.mock('../../hooks/useAttentionItems', () => ({
  useAttentionItems: vi.fn(() => ({ items: [], count: 0, isLoading: false, dismissFailure: vi.fn() })),
}))

vi.mock('../../hooks/queries/servers', () => ({
  useMcpServers: vi.fn(() => ({ data: [] })),
}))

import Layout from './Layout'
import { useSSE } from '../../hooks/useSSE'
import { useCurrentUser } from '../../hooks/queries/users'
import { useAttentionItems } from '../../hooks/useAttentionItems'
import { useMcpServers } from '../../hooks/queries/servers'

function makeClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function renderLayout(initialPath = '/dashboard') {
  const qc = makeClient()
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[initialPath]}>
        <Layout />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('Layout', () => {
  beforeEach(() => {
    vi.mocked(useSSE).mockReturnValue({ connectionState: 'connected' })
    vi.mocked(useCurrentUser).mockReturnValue({ data: { id: '1', username: 'alice', roles: ['admin'] } } as ReturnType<typeof useCurrentUser>)
    vi.mocked(useAttentionItems).mockReturnValue({ items: [], count: 0, isLoading: false, dismissFailure: vi.fn() })
    vi.mocked(useMcpServers).mockReturnValue({ data: [] } as unknown as ReturnType<typeof useMcpServers>)
  })

  it('renders general nav items and admin section for admin user', () => {
    renderLayout()
    expect(screen.getByRole('link', { name: /control center/i })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /run history/i })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /agents/i })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /tools/i })).toBeInTheDocument()
    expect(screen.getAllByText('Admin').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByRole('link', { name: /users/i })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /models/i })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /system/i })).toBeInTheDocument()
  })

  it('active nav item has active class', () => {
    renderLayout('/dashboard')
    const dashboardLink = screen.getByRole('link', { name: /control center/i })
    expect(dashboardLink.className).toContain('navLinkActive')
  })

  it('runs link is active when path is exactly /runs', () => {
    renderLayout('/runs')
    const runsLink = screen.getByRole('link', { name: /run history/i })
    expect(runsLink.className).toContain('navLinkActive')
  })

  // ---- Footer: user account row ----

  it('footer renders user avatar with initial, username, and role', () => {
    renderLayout()
    expect(screen.getByText('A')).toBeInTheDocument()
    expect(screen.getByText('alice')).toBeInTheDocument()
    const adminTexts = screen.getAllByText('Admin')
    expect(adminTexts.length).toBeGreaterThanOrEqual(1)
  })

  it('footer opens user menu on click', () => {
    renderLayout()
    const footer = screen.getByRole('button', { name: /user menu/i })
    fireEvent.click(footer)
    expect(screen.getByRole('menuitem', { name: /^settings$/i })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: /log out/i })).toBeInTheDocument()
  })

  it('footer shows fallback avatar and text when user is loading', () => {
    vi.mocked(useCurrentUser).mockReturnValue({ data: undefined, isLoading: true } as ReturnType<typeof useCurrentUser>)
    renderLayout()
    expect(screen.getByText('?')).toBeInTheDocument()
    const userFallbacks = screen.getAllByText('User')
    expect(userFallbacks.length).toBeGreaterThanOrEqual(2)
  })

  // ---- Nav-level status indicators ----

  it('approval pulse class applied to Control Center when items pending', () => {
    vi.mocked(useAttentionItems).mockReturnValue({
      items: [{} as never, {} as never],
      count: 2,
      isLoading: false,
      dismissFailure: vi.fn(),
    })
    renderLayout()
    const link = screen.getByRole('link', { name: /control center/i })
    expect(link.className).toContain('navLinkNeedsApproval')
  })

  it('MCP unhealthy class applied to Tools when server has null last_discovered_at', () => {
    vi.mocked(useMcpServers).mockReturnValue({ data: [{ last_discovered_at: null }] } as ReturnType<typeof useMcpServers>)
    renderLayout()
    const link = screen.getByRole('link', { name: /tools/i })
    expect(link.className).toContain('navLinkMcpUnhealthy')
  })

  // ---- Disconnect banner in content area ----

  it('disconnect banner shown in content area when reconnecting', () => {
    vi.mocked(useSSE).mockReturnValue({ connectionState: 'reconnecting' })
    renderLayout()
    const banner = screen.getByRole('status')
    expect(banner).toBeInTheDocument()
    expect(banner).toHaveTextContent('Connection lost — reconnecting…')
  })

  it('disconnect banner uses critical style when disconnected', () => {
    vi.mocked(useSSE).mockReturnValue({ connectionState: 'disconnected' })
    renderLayout()
    const banner = screen.getByRole('status')
    expect(banner.className).toContain('disconnectBannerCritical')
  })
})
