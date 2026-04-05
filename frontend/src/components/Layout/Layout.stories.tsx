import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '@/tokens.css'
import Layout from './Layout'

function PageContent({ title }: { title: string }) {
  return (
    <div style={{ padding: '32px' }}>
      <h1 style={{ margin: 0, fontSize: '24px', fontWeight: 600, color: 'var(--text-primary)' }}>
        {title}
      </h1>
      <p style={{ marginTop: '8px', color: 'var(--text-second)' }}>Page content goes here.</p>
    </div>
  )
}

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

function SidebarStory({ initialPath = '/dashboard' }: { initialPath?: string }) {
  return (
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialPath]}>
        <Routes>
          <Route element={<Layout />}>
            <Route path="/dashboard" element={<PageContent title="Dashboard" />} />
            <Route path="/runs" element={<PageContent title="Runs" />} />
            <Route path="/runs/:id" element={<PageContent title="Run Detail" />} />
            <Route path="/agents" element={<PageContent title="Agents" />} />
            <Route path="/agents/new" element={<PageContent title="New Agent" />} />
            <Route path="/tools" element={<PageContent title="Tools" />} />
            <Route path="/settings" element={<PageContent title="Settings" />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  )
}

const meta: Meta = {
  title: 'Layout/Sidebar',
  parameters: {
    layout: 'fullscreen',
  },
  // Clear sidebar collapse state before each story so stories don't leak into each other
  loaders: [
    async () => {
      localStorage.removeItem('gleipnir-sidebar-collapsed')
      return {}
    },
  ],
}

export default meta
type Story = StoryObj

export const Expanded: Story = {
  render: () => <SidebarStory initialPath="/dashboard" />,
}

export const Collapsed: Story = {
  render: () => <SidebarStory initialPath="/dashboard" />,
  loaders: [
    async () => {
      localStorage.setItem('gleipnir-sidebar-collapsed', 'true')
      return {}
    },
  ],
}

export const ActiveRuns: Story = {
  render: () => <SidebarStory initialPath="/runs" />,
}

export const ActiveAgents: Story = {
  render: () => <SidebarStory initialPath="/agents" />,
}

export const ActiveTools: Story = {
  render: () => <SidebarStory initialPath="/tools" />,
}

export const WithPendingApprovals: Story = {
  render: () => <SidebarStory initialPath="/dashboard" />,
  beforeEach: async () => {
    const { vi } = await import('vitest')
    const mod = await import('../../hooks/useAttentionItems')
    vi.spyOn(mod, 'useAttentionItems').mockReturnValue({
      items: [{} as never, {} as never, {} as never],
      count: 3,
      isLoading: false,
      dismissFailure: vi.fn(),
    })
    return () => vi.restoreAllMocks()
  },
}

export const WithUnhealthyServers: Story = {
  render: () => <SidebarStory initialPath="/tools" />,
  beforeEach: async () => {
    const { vi } = await import('vitest')
    const mod = await import('../../hooks/queries/servers')
    vi.spyOn(mod, 'useMcpServers').mockReturnValue({
      data: [{ id: '1', url: 'http://example.com', last_discovered_at: null }],
    } as ReturnType<typeof mod.useMcpServers>)
    return () => vi.restoreAllMocks()
  },
}

export const WithBothAlerts: Story = {
  render: () => <SidebarStory initialPath="/dashboard" />,
  beforeEach: async () => {
    const { vi } = await import('vitest')
    const attMod = await import('../../hooks/useAttentionItems')
    const mcpMod = await import('../../hooks/queries/servers')
    vi.spyOn(attMod, 'useAttentionItems').mockReturnValue({
      items: [{} as never],
      count: 1,
      isLoading: false,
      dismissFailure: vi.fn(),
    })
    vi.spyOn(mcpMod, 'useMcpServers').mockReturnValue({
      data: [{ id: '1', url: 'http://example.com', last_discovered_at: null }],
    } as ReturnType<typeof mcpMod.useMcpServers>)
    return () => vi.restoreAllMocks()
  },
}

export const Disconnected: Story = {
  render: () => <SidebarStory initialPath="/dashboard" />,
  beforeEach: async () => {
    const { vi } = await import('vitest')
    const mod = await import('../../hooks/useSSE')
    vi.spyOn(mod, 'useSSE').mockReturnValue({ connectionState: 'disconnected' })
    return () => vi.restoreAllMocks()
  },
}

export const Reconnecting: Story = {
  render: () => <SidebarStory initialPath="/dashboard" />,
  beforeEach: async () => {
    const { vi } = await import('vitest')
    const mod = await import('../../hooks/useSSE')
    vi.spyOn(mod, 'useSSE').mockReturnValue({ connectionState: 'reconnecting' })
    return () => vi.restoreAllMocks()
  },
}

export const CollapsedWithAlerts: Story = {
  render: () => <SidebarStory initialPath="/dashboard" />,
  loaders: [
    async () => {
      localStorage.setItem('gleipnir-sidebar-collapsed', 'true')
      return {}
    },
  ],
  beforeEach: async () => {
    const { vi } = await import('vitest')
    const attMod = await import('../../hooks/useAttentionItems')
    const mcpMod = await import('../../hooks/queries/servers')
    vi.spyOn(attMod, 'useAttentionItems').mockReturnValue({
      items: [{} as never, {} as never],
      count: 2,
      isLoading: false,
      dismissFailure: vi.fn(),
    })
    vi.spyOn(mcpMod, 'useMcpServers').mockReturnValue({
      data: [{ id: '1', url: 'http://example.com', last_discovered_at: null }],
    } as ReturnType<typeof mcpMod.useMcpServers>)
    return () => vi.restoreAllMocks()
  },
}
