import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { ApprovalActions } from './ApprovalActions'
import { queryKeys } from '@/hooks/queryKeys'

// makeQueryClient creates a fresh QueryClient pre-seeded with the current
// user so that ApprovalActions can check role-based visibility without
// making a real network request.
function makeQueryClient(roles: string[]): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.currentUser.all, { id: '1', username: 'testuser', roles })
  return qc
}

const meta: Meta<typeof ApprovalActions> = {
  title: 'RunDetail/ApprovalActions',
  component: ApprovalActions,
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
type Story = StoryObj<typeof ApprovalActions>

// ApproverWaiting — an approver role sees the Approve / Deny buttons.
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
    runStatus: 'waiting_for_approval',
  },
}

// AdminWaiting — admin also has approval rights.
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
    runStatus: 'waiting_for_approval',
  },
}

// OperatorWaiting — operators cannot approve; component renders nothing.
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
    runStatus: 'waiting_for_approval',
  },
}

// NotWaiting — run is complete; component renders nothing regardless of role.
export const NotWaiting: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(['approver'])}>
        <Story />
      </QueryClientProvider>
    ),
  ],
  args: {
    runId: 'run-1',
    runStatus: 'complete',
  },
}
