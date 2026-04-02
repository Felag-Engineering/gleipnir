import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { AttentionQueue } from './AttentionQueue'
import type { AttentionItem } from '@/hooks/useAttentionItems'
import storyStyles from '../dashboard-stories.module.css'

const meta: Meta<typeof AttentionQueue> = {
  title: 'Dashboard/AttentionQueue',
  component: AttentionQueue,
  decorators: [
    (Story) => (
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <div className={storyStyles.storyWrapper}>
            <Story />
          </div>
        </MemoryRouter>
      </QueryClientProvider>
    ),
  ],
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/runs/:runId/approval', () =>
          HttpResponse.json({ data: { run_id: 'r1', decision: 'approved' } }),
        ),
      ],
    },
  },
}

export default meta
type Story = StoryObj<typeof AttentionQueue>

const now = new Date()

const APPROVAL_ITEM: AttentionItem = {
  type: 'approval',
  request_id: 'ar1',
  run_id: 'r1',
  policy_id: 'p1',
  policy_name: 'deploy-staging',
  tool_name: 'deploy_tool',
  message: '',
  expires_at: new Date(now.getTime() + 2.5 * 60 * 1000).toISOString(),
  created_at: new Date(now.getTime() - 10 * 60 * 1000).toISOString(),
  sortKey: now.getTime() + 2.5 * 60 * 1000,
}

const FEEDBACK_ITEM: AttentionItem = {
  type: 'feedback',
  request_id: 'fr1',
  run_id: 'r2',
  policy_id: 'p2',
  policy_name: 'log-anomalies',
  tool_name: 'ask_operator',
  message: 'I found 23 anomalous log entries in the last hour. Should I escalate to PagerDuty or wait for the next rotation?',
  expires_at: new Date(now.getTime() + 14 * 60 * 1000).toISOString(),
  created_at: new Date(now.getTime() - 5 * 60 * 1000).toISOString(),
  sortKey: now.getTime() + 14 * 60 * 1000,
}

const FAILURE_ITEM: AttentionItem = {
  type: 'failure',
  request_id: '',
  run_id: 'r3',
  policy_id: 'p3',
  policy_name: 'backup-db',
  tool_name: '',
  message: 'MCP tool call timed out after 30s',
  expires_at: null,
  created_at: new Date(now.getTime() - 2 * 60 * 60 * 1000).toISOString(),
  sortKey: now.getTime() - 2 * 60 * 60 * 1000 + 24 * 60 * 60 * 1000,
}

const EXPIRED_APPROVAL: AttentionItem = {
  ...APPROVAL_ITEM,
  request_id: 'ar2',
  run_id: 'r4',
  policy_name: 'nightly-backup',
  expires_at: new Date(now.getTime() + 90 * 1000).toISOString(), // 90 seconds — urgent
  sortKey: now.getTime() + 90 * 1000,
}

const ALL_ITEMS = [APPROVAL_ITEM, FEEDBACK_ITEM, FAILURE_ITEM]

export const Default: Story = {
  args: { items: ALL_ITEMS, count: ALL_ITEMS.length, onDismiss: () => {} },
}

export const AllApprovals: Story = {
  args: {
    items: [APPROVAL_ITEM, EXPIRED_APPROVAL],
    count: 2,
    onDismiss: () => {},
  },
}

export const AllFailures: Story = {
  args: {
    items: [FAILURE_ITEM, { ...FAILURE_ITEM, run_id: 'r5', request_id: 'r5', policy_name: 'sync-github', sortKey: FAILURE_ITEM.sortKey + 1000 }],
    count: 2,
    onDismiss: () => {},
  },
}

export const SingleItem: Story = {
  args: { items: [FEEDBACK_ITEM], count: 1, onDismiss: () => {} },
}

export const ExpiredCountdown: Story = {
  args: { items: [EXPIRED_APPROVAL], count: 1, onDismiss: () => {} },
}
