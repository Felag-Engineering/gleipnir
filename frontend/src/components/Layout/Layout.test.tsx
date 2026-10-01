import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router'

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

  // ---- Mobile drawer toggle (#704) ----

  it('renders a keyboard-accessible menu toggle collapsed by default', () => {
    renderLayout()
    const toggle = screen.getByRole('button', { name: /open navigation menu/i })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(toggle).toHaveAttribute('aria-controls', 'app-sidebar')
    // Sidebar renders but is not in the open state until toggled.
    const sidebar = document.getElementById('app-sidebar')
    expect(sidebar?.className).not.toContain('sidebarOpen')
  })

  it('toggle opens the drawer, exposing the backdrop and open state', () => {
    renderLayout()
    const toggle = screen.getByRole('button', { name: /open navigation menu/i })
    fireEvent.click(toggle)
    const openToggle = screen.getByRole('button', { name: /close navigation menu/i })
    expect(openToggle).toHaveAttribute('aria-expanded', 'true')
    const sidebar = document.getElementById('app-sidebar')
    expect(sidebar?.className).toContain('sidebarOpen')
  })

  it('Escape closes the open drawer', () => {
    renderLayout()
    fireEvent.click(screen.getByRole('button', { name: /open navigation menu/i }))
    expect(document.getElementById('app-sidebar')?.className).toContain('sidebarOpen')
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(document.getElementById('app-sidebar')?.className).not.toContain('sidebarOpen')
    expect(screen.getByRole('button', { name: /open navigation menu/i })).toHaveAttribute('aria-expanded', 'false')
  })

  it('outside-click on the backdrop closes the drawer', () => {
    const { container } = renderLayout()
    fireEvent.click(screen.getByRole('button', { name: /open navigation menu/i }))
    const backdrop = container.querySelector('[class*="backdrop"]') as HTMLElement
    expect(backdrop).not.toBeNull()
    fireEvent.click(backdrop)
    expect(document.getElementById('app-sidebar')?.className).not.toContain('sidebarOpen')
  })

  it('navigation closes the drawer', () => {
    renderLayout()
    fireEvent.click(screen.getByRole('button', { name: /open navigation menu/i }))
    expect(document.getElementById('app-sidebar')?.className).toContain('sidebarOpen')
    fireEvent.click(screen.getByRole('link', { name: /run history/i }))
    expect(document.getElementById('app-sidebar')?.className).not.toContain('sidebarOpen')
  })
})

// Each role's sidebar, checked against the RequireRole gates in
// internal/http/api/router.go: a link is shown exactly when the page's primary
// read is allowed for that role. Roles are given the way the API assigns them
// (an operator also holds approver and auditor).
describe('Layout — sidebar per role', () => {
  const ALL_LINKS = ['Control Center', 'Run History', 'Agents', 'Tools', 'Users', 'Models', 'Audiences', 'Plugins', 'System']

  const CASES: { name: string; roles: string[]; visible: string[]; toolsHealth: boolean }[] = [
    {
      name: 'admin',
      roles: ['admin', 'operator', 'approver', 'auditor'],
      visible: ALL_LINKS,
      toolsHealth: true,
    },
    {
      name: 'operator',
      roles: ['operator', 'approver', 'auditor'],
      visible: ['Control Center', 'Run History', 'Agents', 'Tools', 'Audiences'],
      toolsHealth: true,
    },
    {
      name: 'approver',
      roles: ['approver', 'auditor'],
      visible: ['Control Center', 'Run History', 'Agents', 'Tools', 'Audiences'],
      toolsHealth: true,
    },
    {
      name: 'approver only',
      roles: ['approver'],
      visible: ['Control Center', 'Run History'],
      toolsHealth: false,
    },
    {
      name: 'auditor',
      roles: ['auditor'],
      visible: ['Control Center', 'Run History', 'Agents', 'Tools', 'Audiences'],
      toolsHealth: true,
    },
  ]

  beforeEach(() => {
    vi.mocked(useSSE).mockReturnValue({ connectionState: 'connected' })
    vi.mocked(useAttentionItems).mockReturnValue({ items: [], count: 0, isLoading: false, dismissFailure: vi.fn() })
    vi.mocked(useMcpServers).mockClear()
    vi.mocked(useMcpServers).mockReturnValue({ data: [] } as unknown as ReturnType<typeof useMcpServers>)
  })

  it.each(CASES)('$name sees exactly the pages its role can use', ({ roles, visible, toolsHealth }) => {
    vi.mocked(useCurrentUser).mockReturnValue({ data: { id: '1', username: 'u', roles } } as ReturnType<typeof useCurrentUser>)
    renderLayout()
    const nav = screen.getByRole('navigation', { name: 'Main navigation' })
    const shown = within(nav).getAllByRole('link').map(l => l.textContent)
    expect(shown).toEqual(visible)
    // The Admin heading appears only above at least one admin-section entry.
    const hasAdminSection = visible.some(l => ['Users', 'Models', 'Audiences', 'Plugins', 'System'].includes(l))
    expect(within(nav).queryByText('Admin') !== null).toBe(hasAdminSection)
    // The Tools health dot reads the server list, which 403s for a role that
    // cannot open Tools — so it is not asked for.
    expect(vi.mocked(useMcpServers)).toHaveBeenLastCalledWith({ enabled: toolsHealth })
  })

  it('shows only the ungated Control Center before the current user is known', () => {
    vi.mocked(useCurrentUser).mockReturnValue({ data: undefined } as ReturnType<typeof useCurrentUser>)
    renderLayout()
    const nav = screen.getByRole('navigation', { name: 'Main navigation' })
    expect(within(nav).getAllByRole('link').map(l => l.textContent)).toEqual(['Control Center'])
    expect(vi.mocked(useMcpServers)).toHaveBeenLastCalledWith({ enabled: false })
  })
})
