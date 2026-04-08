import type { Meta, StoryObj } from '@storybook/react-vite'
import { fn } from 'storybook/test'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { AttentionItem } from './AttentionItem'
import type { AttentionItem as AttentionItemType } from '@/hooks/useAttentionItems'
import storyStyles from '../dashboard-stories.module.css'

const meta: Meta<typeof AttentionItem> = {
  title: 'Dashboard/AttentionItem',
  component: AttentionItem,
  decorators: [
    (Story) => (
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter>
          <div className={storyStyles.storyWrapper}>
            <Story />
          </div>
        </MemoryRouter>
      </QueryClientProvider>
    ),
  ],
  args: {
    onDismiss: fn(),
  },
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/runs/:runId/approval', () =>
          HttpResponse.json({ data: { run_id: 'run-1', decision: 'approved' } }),
        ),
      ],
    },
  },
}

export default meta
type Story = StoryObj<typeof AttentionItem>

const now = new Date()

const APPROVAL_ITEM: AttentionItemType = {
  type: 'approval',
  request_id: 'ar-1',
  run_id: 'run-1',
  policy_id: 'pol-1',
  policy_name: 'deploy-staging',
  tool_name: 'k8s.rolling_deploy',
  message: '',
  expires_at: new Date(now.getTime() + 8 * 60 * 1000).toISOString(),
  created_at: new Date(now.getTime() - 2 * 60 * 1000).toISOString(),
  sortKey: now.getTime() + 8 * 60 * 1000,
}

const FEEDBACK_ITEM: AttentionItemType = {
  type: 'feedback',
  request_id: 'fr-1',
  run_id: 'run-2',
  policy_id: 'pol-2',
  policy_name: 'log-anomalies',
  tool_name: 'gleipnir.ask_operator',
  message: 'I found 23 anomalous log entries in the last hour. Should I escalate to PagerDuty or wait for the next rotation?',
  expires_at: new Date(now.getTime() + 14 * 60 * 1000).toISOString(),
  created_at: new Date(now.getTime() - 5 * 60 * 1000).toISOString(),
  sortKey: now.getTime() + 14 * 60 * 1000,
}

const FAILURE_ITEM: AttentionItemType = {
  type: 'failure',
  request_id: '',
  run_id: 'run-3',
  policy_id: 'pol-3',
  policy_name: 'backup-db',
  tool_name: '',
  message: 'MCP tool call timed out after 30s',
  expires_at: null,
  created_at: new Date(now.getTime() - 2 * 60 * 60 * 1000).toISOString(),
  sortKey: now.getTime() - 2 * 60 * 60 * 1000 + 24 * 60 * 60 * 1000,
}

// ApprovalItem — shows Approve/Reject buttons with a countdown.
export const ApprovalItem: Story = {
  args: { item: APPROVAL_ITEM },
}

// ApprovalUrgent — expires in under 90 seconds, so the countdown turns red.
export const ApprovalUrgent: Story = {
  args: {
    item: {
      ...APPROVAL_ITEM,
      request_id: 'ar-2',
      run_id: 'run-4',
      expires_at: new Date(now.getTime() + 75 * 1000).toISOString(),
      sortKey: now.getTime() + 75 * 1000,
    },
  },
}

// FeedbackItem — shows a Respond button and the question preview.
export const FeedbackItem: Story = {
  args: { item: FEEDBACK_ITEM },
}

// FailureItem — shows View Run and a dismiss button; no countdown.
export const FailureItem: Story = {
  args: { item: FAILURE_ITEM },
}

// LongPolicyName — exercises text truncation in the header.
export const LongPolicyName: Story = {
  args: {
    item: {
      ...APPROVAL_ITEM,
      policy_name: 'infrastructure-production-database-migration-validate-and-apply-schema',
    },
  },
}
