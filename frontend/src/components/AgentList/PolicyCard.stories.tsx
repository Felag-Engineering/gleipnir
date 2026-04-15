import type { Meta, StoryObj } from '@storybook/react-vite'
import { fn } from 'storybook/test'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import type { ApiPolicyListItem } from '@/api/types'
import { PolicyCard } from './PolicyCard'
import { queryKeys } from '@/hooks/queryKeys'

// makeQueryClient pre-seeds the policy detail and runs list caches so that
// PolicyCardExpanded (rendered when the card is expanded) does not make real
// network requests in Storybook.
function makeQueryClient(policyId: string): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  qc.setQueryData(queryKeys.policies.detail(policyId), {
    id: policyId,
    name: 'Nightly Backup Check',
    trigger_type: 'scheduled',
    folder: 'Infrastructure',
    yaml: `
name: Nightly Backup Check
trigger:
  type: scheduled
  fire_at:
    - "2026-04-09T02:00:00Z"
identity:
  description: Verifies that nightly database backups completed successfully.
capabilities:
  tools:
    - tool_id: backup-server.check_status
      approval: none
limits:
  max_tokens_per_run: 50000
  max_tool_calls_per_run: 20
concurrency:
  concurrency: skip
`,
    created_at: '2026-03-01T00:00:00Z',
    updated_at: '2026-04-01T00:00:00Z',
    paused_at: null,
  })

  // Key object must stay in sync with what useRuns constructs — a mismatch silently misses the preseed.
  qc.setQueryData(queryKeys.runs.list({ policy_id: policyId, limit: '5', sort: 'started_at', order: 'desc' }), {
    runs: [
      { id: 'run-a', policy_id: policyId, status: 'complete', trigger_type: 'scheduled', started_at: '2026-04-08T02:00:00Z', completed_at: '2026-04-08T02:04:00Z', token_cost: 1200, error: null, created_at: '2026-04-08T02:00:00Z', system_prompt: null, model: 'claude-sonnet-4-20250514' },
      { id: 'run-b', policy_id: policyId, status: 'failed', trigger_type: 'scheduled', started_at: '2026-04-07T02:00:00Z', completed_at: '2026-04-07T02:01:00Z', token_cost: 400, error: 'Timeout', created_at: '2026-04-07T02:00:00Z', system_prompt: null, model: 'claude-sonnet-4-20250514' },
      { id: 'run-c', policy_id: policyId, status: 'complete', trigger_type: 'scheduled', started_at: '2026-04-06T02:00:00Z', completed_at: '2026-04-06T02:03:30Z', token_cost: 980, error: null, created_at: '2026-04-06T02:00:00Z', system_prompt: null, model: 'claude-sonnet-4-20250514' },
    ],
    total: 3,
  })

  return qc
}

const BASE_POLICY: ApiPolicyListItem = {
  id: 'pol-1',
  name: 'Nightly Backup Check',
  trigger_type: 'scheduled',
  folder: 'Infrastructure',
  model: 'claude-sonnet-4-20250514',
  tool_count: 1,
  tool_refs: ['backup-server.check_status'],
  avg_token_cost: 1060,
  run_count: 3,
  created_at: '2026-03-01T00:00:00Z',
  updated_at: '2026-04-01T00:00:00Z',
  paused_at: null,
  latest_run: {
    id: 'run-a',
    status: 'complete',
    started_at: '2026-04-08T02:00:00Z',
    token_cost: 1200,
  },
}

const meta: Meta<typeof PolicyCard> = {
  title: 'Components/PolicyCard',
  component: PolicyCard,
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient('pol-1')}>
        <MemoryRouter>
          <Story />
        </MemoryRouter>
      </QueryClientProvider>
    ),
  ],
  args: {
    onTrigger: fn(),
  },
}

export default meta
type Story = StoryObj<typeof PolicyCard>

export const CollapsedComplete: Story = {
  args: { policy: BASE_POLICY },
}

export const CollapsedRunning: Story = {
  args: {
    policy: {
      ...BASE_POLICY,
      latest_run: { id: 'run-x', status: 'running', started_at: new Date(Date.now() - 90_000).toISOString(), token_cost: 0 },
    },
  },
}

export const CollapsedWaitingApproval: Story = {
  args: {
    policy: {
      ...BASE_POLICY,
      latest_run: { id: 'run-y', status: 'waiting_for_approval', started_at: new Date(Date.now() - 5 * 60_000).toISOString(), token_cost: 0 },
    },
  },
}

export const CollapsedFailed: Story = {
  args: {
    policy: {
      ...BASE_POLICY,
      latest_run: { id: 'run-z', status: 'failed', started_at: new Date(Date.now() - 3 * 60_000).toISOString(), token_cost: 400 },
    },
  },
}

export const NoLatestRun: Story = {
  args: {
    policy: {
      ...BASE_POLICY,
      latest_run: null,
    },
  },
}
