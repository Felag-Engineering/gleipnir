import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

vi.mock('../../hooks/useSSE', () => ({
  useSSE: vi.fn(() => ({ connectionState: 'connected' })),
}))

import Layout from './Layout'

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
  })

  it('renders 4 nav items', () => {
    renderLayout()
    expect(screen.getByRole('link', { name: /dashboard/i })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /runs/i })).toBeInTheDocument()
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
    const dashboardLink = screen.getByRole('link', { name: /dashboard/i })
    expect(dashboardLink.className).toContain('navLinkActive')
  })

  it('runs link is active when path is exactly /runs', () => {
    renderLayout('/runs')
    const runsLink = screen.getByRole('link', { name: /runs/i })
    expect(runsLink.className).toContain('navLinkActive')
  })

  it('runs link is NOT active for nested paths like /runs/some-id', () => {
    renderLayout('/runs/some-id')
    const runsLink = screen.getByRole('link', { name: /runs/i })
    expect(runsLink.className).not.toContain('navLinkActive')
  })

  it('connection banner renders in sidebar footer when collapsed', () => {
    localStorage.setItem('gleipnir-sidebar-collapsed', 'true')
    renderLayout()
    // compact mode always renders the dot indicator with role="status"
    expect(screen.getByRole('status')).toBeInTheDocument()
  })
})
