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
            <Route path="/policies" element={<PageContent title="Policies" />} />
            <Route path="/policies/new" element={<PageContent title="New Policy" />} />
            <Route path="/tools" element={<PageContent title="Tools" />} />
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

export const ActivePolicies: Story = {
  render: () => <SidebarStory initialPath="/policies" />,
}

export const ActiveTools: Story = {
  render: () => <SidebarStory initialPath="/tools" />,
}
