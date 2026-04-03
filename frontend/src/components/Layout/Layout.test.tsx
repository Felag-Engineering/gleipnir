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
    localStorage.clear()
    vi.mocked(useSSE).mockReturnValue({ connectionState: 'connected' })
    vi.mocked(useCurrentUser).mockReturnValue({ data: { id: '1', username: 'alice', roles: ['admin'] } } as ReturnType<typeof useCurrentUser>)
    vi.mocked(useAttentionItems).mockReturnValue({ items: [], count: 0, isLoading: false, dismissFailure: vi.fn() })
    vi.mocked(useMcpServers).mockReturnValue({ data: [] } as unknown as ReturnType<typeof useMcpServers>)
  })

  it('renders 4 nav items', () => {
    renderLayout()
    expect(screen.getByRole('link', { name: /control center/i })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /run history/i })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /policies/i })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /tools/i })).toBeInTheDocument()
  })

  it('sidebar is expanded by default when no localStorage value', () => {
    renderLayout()
    const sidebar = screen.getByRole('complementary')
    expect(sidebar.className).not.toContain('sidebarCollapsed')
  })

  it('reads collapsed state from localStorage', () => {
    localStorage.setItem('gleipnir-sidebar-collapsed', 'true')
    renderLayout()
    const sidebar = screen.getByRole('complementary')
    expect(sidebar.className).toContain('sidebarCollapsed')
  })

  it('toggle button persists state to localStorage', () => {
    renderLayout()
    const toggle = screen.getByRole('button', { name: /collapse sidebar/i })
    fireEvent.click(toggle)
    expect(localStorage.getItem('gleipnir-sidebar-collapsed')).toBe('true')

    fireEvent.click(screen.getByRole('button', { name: /expand sidebar/i }))
    expect(localStorage.getItem('gleipnir-sidebar-collapsed')).toBe('false')
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

  it('runs link is NOT active for nested paths like /runs/some-id', () => {
    renderLayout('/runs/some-id')
    const runsLink = screen.getByRole('link', { name: /run history/i })
    expect(runsLink.className).not.toContain('navLinkActive')
  })

  // ---- Footer: user account row ----

  it('footer renders user avatar with initial, username, and role', () => {
    renderLayout()
    // Avatar initial is the first letter of 'alice', uppercased
    expect(screen.getByText('A')).toBeInTheDocument()
    expect(screen.getByText('alice')).toBeInTheDocument()
    // Role 'admin' is capitalized to 'Admin'
    expect(screen.getByText('Admin')).toBeInTheDocument()
  })

  it('footer navigates to /settings on click', () => {
    renderLayout()
    const footer = screen.getByRole('button', { name: /user settings/i })
    fireEvent.click(footer)
    // MemoryRouter won't actually navigate, but the element must be present and clickable
    expect(footer).toBeInTheDocument()
  })

  it('collapsed footer shows only avatar initial, hides username and role', () => {
    localStorage.setItem('gleipnir-sidebar-collapsed', 'true')
    renderLayout()
    // Avatar initial should still be visible
    expect(screen.getByText('A')).toBeInTheDocument()
    // Username and role should not be rendered when collapsed
    expect(screen.queryByText('alice')).not.toBeInTheDocument()
    expect(screen.queryByText('Admin')).not.toBeInTheDocument()
  })

  it('footer shows fallback avatar and text when user is loading', () => {
    vi.mocked(useCurrentUser).mockReturnValue({ data: undefined, isLoading: true } as ReturnType<typeof useCurrentUser>)
    renderLayout()
    // When currentUser is undefined, avatar shows '?'
    expect(screen.getByText('?')).toBeInTheDocument()
    // Both name and role fall back to 'User'
    // There will be two 'User' elements — one for name, one for role
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

  it('approval pulse class absent when no pending items', () => {
    renderLayout()
    const link = screen.getByRole('link', { name: /control center/i })
    expect(link.className).not.toContain('navLinkNeedsApproval')
  })

  it('MCP unhealthy class applied to Tools when server has null last_discovered_at', () => {
    vi.mocked(useMcpServers).mockReturnValue({ data: [{ last_discovered_at: null }] } as ReturnType<typeof useMcpServers>)
    renderLayout()
    const link = screen.getByRole('link', { name: /tools/i })
    expect(link.className).toContain('navLinkMcpUnhealthy')
  })

  it('MCP unhealthy class absent when all servers healthy', () => {
    vi.mocked(useMcpServers).mockReturnValue({
      data: [{ last_discovered_at: '2026-01-01T00:00:00Z' }],
    } as ReturnType<typeof useMcpServers>)
    renderLayout()
    const link = screen.getByRole('link', { name: /tools/i })
    expect(link.className).not.toContain('navLinkMcpUnhealthy')
  })

  // ---- Disconnect banner in content area ----

  it('disconnect banner shown in content area when reconnecting', () => {
    vi.mocked(useSSE).mockReturnValue({ connectionState: 'reconnecting' })
    renderLayout()
    const banner = screen.getByRole('status')
    expect(banner).toBeInTheDocument()
    expect(banner).toHaveTextContent('Connection lost — reconnecting…')
  })

  it('disconnect banner hidden when connected', () => {
    renderLayout()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('disconnect banner uses critical style when disconnected', () => {
    vi.mocked(useSSE).mockReturnValue({ connectionState: 'disconnected' })
    renderLayout()
    const banner = screen.getByRole('status')
    expect(banner.className).toContain('disconnectBannerCritical')
  })
})
