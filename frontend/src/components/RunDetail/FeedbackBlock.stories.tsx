import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import type { ApiRunStep } from '@/api/types'
import { parseStep } from './types'
import { FeedbackBlock } from './FeedbackBlock'
import { queryKeys } from '@/hooks/queryKeys'

// makeQueryClient pre-seeds currentUser so FeedbackActions (rendered inside
// FeedbackBlock when the run is waiting_for_feedback) can check role visibility
// without a live API call.
function makeQueryClient(): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.currentUser.all, { id: '1', username: 'operator', roles: ['operator'] })
  return qc
}

function makeRaw(overrides: Partial<ApiRunStep> = {}): ApiRunStep {
  return {
    id: 'step-fb-1',
    run_id: 'run-1',
    step_number: 3,
    type: 'feedback_request',
    content: '{}',
    token_cost: 0,
    created_at: '2026-04-08T10:00:00Z',
    ...overrides,
  }
}

type ParsedStep<T extends ReturnType<typeof parseStep>['type']> = ReturnType<typeof parseStep> & { type: T }

function makeFeedbackStep<T extends ReturnType<typeof parseStep>['type']>(
  overrides: Partial<ApiRunStep> = {},
): ParsedStep<T> {
  return parseStep(makeRaw(overrides)) as ParsedStep<T>
}

const meta: Meta<typeof FeedbackBlock> = {
  title: 'RunDetail/FeedbackBlock',
  component: FeedbackBlock,
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient()}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof FeedbackBlock>

export const RequestShort: Story = {
  args: {
    step: makeFeedbackStep<'feedback_request'>({
      type: 'feedback_request',
      content: JSON.stringify({
        tool: 'gleipnir.ask_operator',
        message: 'Should I proceed with the database migration?',
        feedback_id: 'fb-1',
        expires_at: new Date(Date.now() + 15 * 60 * 1000).toISOString(),
      }),
    }),
    runId: 'run-1',
    runStatus: 'waiting_for_feedback',
  },
}

export const RequestWithContext: Story = {
  args: {
    step: makeFeedbackStep<'feedback_request'>({
      type: 'feedback_request',
      content: JSON.stringify({
        tool: 'gleipnir.ask_operator',
        message: 'Should I escalate the anomalous log entries to PagerDuty?\n\nI found 23 anomalous entries in the last hour. Error rates have increased by 15% compared to the rolling baseline. The affected service is auth-gateway.',
        feedback_id: 'fb-2',
        expires_at: new Date(Date.now() + 8 * 60 * 1000).toISOString(),
      }),
    }),
    runId: 'run-1',
    runStatus: 'waiting_for_feedback',
  },
}

// RequestResolved — run is no longer in waiting_for_feedback so no countdown or textarea shows.
export const RequestResolved: Story = {
  args: {
    step: makeFeedbackStep<'feedback_request'>({
      type: 'feedback_request',
      content: JSON.stringify({
        tool: 'gleipnir.ask_operator',
        message: 'Should I proceed with the database migration?',
        feedback_id: 'fb-1',
        expires_at: new Date(Date.now() + 15 * 60 * 1000).toISOString(),
      }),
    }),
    runId: 'run-1',
    runStatus: 'complete',
  },
}

export const Response: Story = {
  args: {
    step: makeFeedbackStep<'feedback_response'>({
      type: 'feedback_response',
      content: JSON.stringify({
        feedback_id: 'fb-1',
        response: 'Yes, proceed with the migration. I have verified the backup is current.',
      }),
    }),
    runId: 'run-1',
    runStatus: 'running',
  },
}

export const RequestLongContent: Story = {
  args: {
    step: makeFeedbackStep<'feedback_request'>({
      type: 'feedback_request',
      content: JSON.stringify({
        tool: 'gleipnir.ask_operator',
        message: 'Should I terminate the long-running query in the analytics cluster?\n\nThe query has been running for 4 hours 37 minutes and is consuming 94% of available CPU on the analytics-01 node. The query belongs to the reporting pipeline for the monthly finance report. Terminating it will require the report generation job to restart from scratch, adding approximately 3-4 hours to the total processing time. However, leaving it running is causing elevated latency for all other queries against the same cluster. The SLA for the finance report is end of business today, which leaves 6 hours.',
        feedback_id: 'fb-3',
        expires_at: new Date(Date.now() + 4 * 60 * 1000).toISOString(),
      }),
    }),
    runId: 'run-1',
    runStatus: 'waiting_for_feedback',
  },
}
