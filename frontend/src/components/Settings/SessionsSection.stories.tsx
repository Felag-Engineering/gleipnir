import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { SessionsSection } from './SessionsSection'
import { queryKeys } from '@/hooks/queryKeys'
import type { ApiSession } from '@/api/types'

const CHROME_SESSION: ApiSession = {
  id: 'sess-1',
  user_agent:
    'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36',
  ip_address: '192.168.1.42',
  created_at: '2026-04-01T10:00:00Z',
  expires_at: '2026-04-08T10:00:00Z',
  is_current: true,
}

const FIREFOX_SESSION: ApiSession = {
  id: 'sess-2',
  user_agent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:124.0) Gecko/20100101 Firefox/124.0',
  ip_address: '10.0.0.5',
  created_at: '2026-03-28T08:30:00Z',
  expires_at: '2026-04-04T08:30:00Z',
  is_current: false,
}

const EDGE_SESSION: ApiSession = {
  id: 'sess-3',
  user_agent:
    'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 Edg/124.0.0.0',
  ip_address: '172.16.0.3',
  created_at: '2026-03-30T14:00:00Z',
  expires_at: '2026-04-06T14:00:00Z',
  is_current: false,
}

function makeQueryClient(sessions: ApiSession[]): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.sessions.all, sessions)
  return qc
}

const meta: Meta<typeof SessionsSection> = {
  title: 'Settings/SessionsSection',
  component: SessionsSection,
  parameters: {
    msw: {
      handlers: [
        http.delete('/api/v1/auth/sessions/:id', () => new HttpResponse(null, { status: 204 })),
      ],
    },
  },
}

export default meta
type Story = StoryObj<typeof SessionsSection>

export const MultipleSessions: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient([CHROME_SESSION, FIREFOX_SESSION, EDGE_SESSION])}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export const CurrentOnly: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient([CHROME_SESSION])}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export const Empty: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient([])}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export const Loading: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}
