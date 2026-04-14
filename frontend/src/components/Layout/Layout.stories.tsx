import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import '@/tokens.css'
import Layout from './Layout'
import { queryKeys } from '@/hooks/queryKeys'

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

function makeLayoutQueryClient(overrides?: {
  currentUser?: { id: string; username: string; roles: string[] }
  attentionItems?: unknown[]
  servers?: unknown[]
}): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  if (overrides?.currentUser) {
    qc.setQueryData(queryKeys.currentUser.all, overrides.currentUser)
  }
  if (overrides?.attentionItems) {
    qc.setQueryData(queryKeys.attention.all, { items: overrides.attentionItems })
  }
  if (overrides?.servers) {
    qc.setQueryData(queryKeys.servers.all, overrides.servers)
  }
  return qc
}

function SidebarStory({
  initialPath = '/dashboard',
  queryClient,
}: {
  initialPath?: string
  queryClient?: QueryClient
}) {
  const qc = queryClient ?? new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return (
    <QueryClientProvider client={qc}>
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
            <Route path="/admin/users" element={<PageContent title="Users" />} />
            <Route path="/admin/models" element={<PageContent title="Models" />} />
            <Route path="/admin/system" element={<PageContent title="System" />} />
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
}

export default meta
type Story = StoryObj

export const Default: Story = {
  render: () => <SidebarStory initialPath="/dashboard" />,
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
  render: () => (
    <SidebarStory
      initialPath="/dashboard"
      queryClient={makeLayoutQueryClient({
        attentionItems: [{} as never, {} as never, {} as never],
      })}
    />
  ),
}

export const WithUnhealthyServers: Story = {
  render: () => (
    <SidebarStory
      initialPath="/tools"
      queryClient={makeLayoutQueryClient({
        servers: [{ id: '1', url: 'http://example.com', last_discovered_at: null }],
      })}
    />
  ),
}

export const WithBothAlerts: Story = {
  render: () => (
    <SidebarStory
      initialPath="/dashboard"
      queryClient={makeLayoutQueryClient({
        attentionItems: [{} as never],
        servers: [{ id: '1', url: 'http://example.com', last_discovered_at: null }],
      })}
    />
  ),
}

export const Disconnected: Story = {
  render: () => <SidebarStory initialPath="/dashboard" />,
}

export const Reconnecting: Story = {
  render: () => <SidebarStory initialPath="/dashboard" />,
}

export const AdminUser: Story = {
  render: () => (
    <SidebarStory
      initialPath="/dashboard"
      queryClient={makeLayoutQueryClient({
        currentUser: { id: '1', username: 'admin', roles: ['admin'] },
      })}
    />
  ),
}

export const ActiveAdminUsers: Story = {
  render: () => (
    <SidebarStory
      initialPath="/admin/users"
      queryClient={makeLayoutQueryClient({
        currentUser: { id: '1', username: 'admin', roles: ['admin'] },
      })}
    />
  ),
}

export const ActiveAdminSystem: Story = {
  render: () => (
    <SidebarStory
      initialPath="/admin/system"
      queryClient={makeLayoutQueryClient({
        currentUser: { id: '1', username: 'admin', roles: ['admin'] },
      })}
    />
  ),
}
