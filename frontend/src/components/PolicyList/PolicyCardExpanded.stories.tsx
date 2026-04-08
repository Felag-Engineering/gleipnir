import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import type { ApiPolicyListItem } from '@/api/types'
import { PolicyCardExpanded } from './PolicyCardExpanded'
import { queryKeys } from '@/hooks/queryKeys'

// Minimal valid YAML for the default story — parses through yamlToFormState
// without errors and exercises the description, capabilities, and limits sections.
const SAMPLE_YAML = `
name: Deploy Staging
trigger:
  type: webhook
identity:
  description: Validates and deploys the latest artifact to the staging environment.
capabilities:
  tools:
    - tool_id: k8s-server.rolling_deploy
      approval: none
    - tool_id: k8s-server.get_pod_status
      approval: none
    - tool_id: gh-server.post_status_check
      approval: required
limits:
  max_tokens_per_run: 80000
  max_tool_calls_per_run: 30
concurrency:
  concurrency: queue
`

const MANY_TOOLS_YAML = `
name: Multi-Tool Agent
trigger:
  type: manual
identity:
  description: Exercises many tools across several servers.
capabilities:
  tools:
    - tool_id: gh-server.create_pr
      approval: none
    - tool_id: gh-server.list_commits
      approval: none
    - tool_id: gh-server.post_comment
      approval: none
    - tool_id: k8s-server.rolling_deploy
      approval: required
    - tool_id: k8s-server.scale_deployment
      approval: required
    - tool_id: slack-server.post_message
      approval: none
limits:
  max_tokens_per_run: 100000
  max_tool_calls_per_run: 50
concurrency:
  concurrency: skip
`

const NO_DESC_YAML = `
name: Minimal Policy
trigger:
  type: manual
capabilities:
  tools: []
limits:
  max_tokens_per_run: 20000
  max_tool_calls_per_run: 10
concurrency:
  concurrency: skip
`

const POLICY_ID = 'pol-expand-1'

const BASE_LIST_ITEM: ApiPolicyListItem = {
  id: POLICY_ID,
  name: 'Deploy Staging',
  trigger_type: 'webhook',
  folder: 'CI/CD',
  model: 'claude-sonnet-4-20250514',
  tool_count: 3,
  tool_refs: ['k8s-server.rolling_deploy', 'k8s-server.get_pod_status', 'gh-server.post_status_check'],
  avg_token_cost: 4500,
  created_at: '2026-03-01T00:00:00Z',
  updated_at: '2026-04-01T00:00:00Z',
  paused_at: null,
  latest_run: {
    id: 'run-latest',
    status: 'complete',
    started_at: '2026-04-08T08:00:00Z',
    token_cost: 4200,
  },
}

const RECENT_RUNS = [
  { id: 'run-1', policy_id: POLICY_ID, status: 'complete', trigger_type: 'webhook', started_at: '2026-04-08T08:00:00Z', completed_at: '2026-04-08T08:05:00Z', token_cost: 4200, error: null, created_at: '2026-04-08T08:00:00Z', system_prompt: null, model: 'claude-sonnet-4-20250514' },
  { id: 'run-2', policy_id: POLICY_ID, status: 'failed', trigger_type: 'webhook', started_at: '2026-04-07T14:00:00Z', completed_at: '2026-04-07T14:01:00Z', token_cost: 800, error: 'Timeout', created_at: '2026-04-07T14:00:00Z', system_prompt: null, model: 'claude-sonnet-4-20250514' },
  { id: 'run-3', policy_id: POLICY_ID, status: 'complete', trigger_type: 'webhook', started_at: '2026-04-06T09:00:00Z', completed_at: '2026-04-06T09:06:00Z', token_cost: 5100, error: null, created_at: '2026-04-06T09:00:00Z', system_prompt: null, model: 'claude-sonnet-4-20250514' },
  { id: 'run-4', policy_id: POLICY_ID, status: 'complete', trigger_type: 'webhook', started_at: '2026-04-05T11:00:00Z', completed_at: '2026-04-05T11:07:00Z', token_cost: 4900, error: null, created_at: '2026-04-05T11:00:00Z', system_prompt: null, model: 'claude-sonnet-4-20250514' },
  { id: 'run-5', policy_id: POLICY_ID, status: 'waiting_for_approval', trigger_type: 'webhook', started_at: '2026-04-04T15:00:00Z', completed_at: null, token_cost: 0, error: null, created_at: '2026-04-04T15:00:00Z', system_prompt: null, model: 'claude-sonnet-4-20250514' },
]

const RUN_LIST_KEY = { policy_id: POLICY_ID, limit: '5', sort: 'started_at', order: 'desc' }

function makeQueryClient(yaml: string, runsData = RECENT_RUNS): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })

  qc.setQueryData(queryKeys.policies.detail(POLICY_ID), {
    id: POLICY_ID,
    name: BASE_LIST_ITEM.name,
    trigger_type: BASE_LIST_ITEM.trigger_type,
    folder: BASE_LIST_ITEM.folder,
    yaml,
    created_at: BASE_LIST_ITEM.created_at,
    updated_at: BASE_LIST_ITEM.updated_at,
    paused_at: null,
  })

  // Key object must stay in sync with what useRuns constructs — a mismatch silently misses the preseed.
  qc.setQueryData(queryKeys.runs.list(RUN_LIST_KEY), {
    runs: runsData,
    total: runsData.length,
  })

  return qc
}

const meta: Meta<typeof PolicyCardExpanded> = {
  title: 'Components/PolicyCardExpanded',
  component: PolicyCardExpanded,
}

export default meta
type Story = StoryObj<typeof PolicyCardExpanded>

export const Default: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(SAMPLE_YAML)}>
        <Story />
      </QueryClientProvider>
    ),
  ],
  args: { policy: BASE_LIST_ITEM },
}

// Loading — a delay handler keeps the detail query in loading state so the
// skeleton placeholder renders for the duration of the story.
export const Loading: Story = {
  parameters: {
    msw: {
      handlers: [
        http.get(`/api/v1/policies/${POLICY_ID}`, async () => {
          await new Promise((resolve) => setTimeout(resolve, 10_000))
          return HttpResponse.json({ data: {} })
        }),
      ],
    },
  },
  decorators: [
    (Story) => (
      // Empty QueryClient so the detail query fires and hits the delay handler.
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <Story />
      </QueryClientProvider>
    ),
  ],
  args: { policy: BASE_LIST_ITEM },
}

// Empty — no runs yet and no tools defined.
export const Empty: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(NO_DESC_YAML, [])}>
        <Story />
      </QueryClientProvider>
    ),
  ],
  args: { policy: { ...BASE_LIST_ITEM, tool_count: 0, tool_refs: [], avg_token_cost: 0, latest_run: null } },
}

// ManyTools — exercises the tool grouping by server display.
export const ManyTools: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(MANY_TOOLS_YAML)}>
        <Story />
      </QueryClientProvider>
    ),
  ],
  args: { policy: { ...BASE_LIST_ITEM, tool_count: 6, name: 'Multi-Tool Agent', trigger_type: 'manual' } },
}

// NoDescription — yaml has no identity.description field.
export const NoDescription: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(NO_DESC_YAML)}>
        <Story />
      </QueryClientProvider>
    ),
  ],
  args: { policy: { ...BASE_LIST_ITEM, name: 'Minimal Policy', trigger_type: 'manual', tool_count: 0, tool_refs: [] } },
}
