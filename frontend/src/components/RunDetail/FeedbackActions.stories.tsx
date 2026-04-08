import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { FeedbackActions } from './FeedbackActions'
import { queryKeys } from '@/hooks/queryKeys'

// makeQueryClient creates a fresh QueryClient pre-seeded with a user so the
// component can check role visibility without a live API call.
function makeQueryClient(roles: string[]): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.currentUser.all, { id: '1', username: 'testuser', roles })
  return qc
}

const meta: Meta<typeof FeedbackActions> = {
  title: 'RunDetail/FeedbackActions',
  component: FeedbackActions,
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/runs/:runId/feedback', () =>
          HttpResponse.json({ data: { run_id: 'run-1' } }),
        ),
      ],
    },
  },
}

export default meta
type Story = StoryObj<typeof FeedbackActions>

// OperatorWaiting — operators can submit feedback responses.
export const OperatorWaiting: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(['operator'])}>
        <Story />
      </QueryClientProvider>
    ),
  ],
  args: {
    runId: 'run-1',
    runStatus: 'waiting_for_feedback',
    feedbackId: 'fb-1',
  },
}

// ApproverWaiting — approvers also have the 'operator' right to respond.
export const ApproverWaiting: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(['approver'])}>
        <Story />
      </QueryClientProvider>
    ),
  ],
  args: {
    runId: 'run-1',
    runStatus: 'waiting_for_feedback',
    feedbackId: 'fb-1',
  },
}

// AdminWaiting — admins can respond to feedback requests.
export const AdminWaiting: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(['admin'])}>
        <Story />
      </QueryClientProvider>
    ),
  ],
  args: {
    runId: 'run-1',
    runStatus: 'waiting_for_feedback',
    feedbackId: 'fb-1',
  },
}

// AuditorWaiting — auditors cannot respond; component renders nothing.
export const AuditorWaiting: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(['auditor'])}>
        <Story />
      </QueryClientProvider>
    ),
  ],
  args: {
    runId: 'run-1',
    runStatus: 'waiting_for_feedback',
    feedbackId: 'fb-1',
  },
}

// NotWaiting — run is not in feedback state; component renders nothing.
export const NotWaiting: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(['operator'])}>
        <Story />
      </QueryClientProvider>
    ),
  ],
  args: {
    runId: 'run-1',
    runStatus: 'running',
    feedbackId: 'fb-1',
  },
}
