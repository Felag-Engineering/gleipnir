import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router-dom'
import '@/tokens.css'
import { ActivityFeed } from './ActivityFeed'
import type { ApiRun } from '@/api/types'

const meta: Meta<typeof ActivityFeed> = {
  title: 'Dashboard/ActivityFeed',
  component: ActivityFeed,
  decorators: [
    (Story) => (
      <MemoryRouter>
        <Story />
      </MemoryRouter>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof ActivityFeed>

const MOCK_RUNS: ApiRun[] = [
  {
    id: 'r1',
    policy_id: 'p1',
    policy_name: 'vikunja-triage',
    status: 'complete',
    trigger_type: 'webhook',
    started_at: '2026-03-16T10:00:00Z',
    completed_at: '2026-03-16T10:05:00Z',
    token_cost: 1200,
    error: null,
    created_at: '2026-03-16T10:00:00Z',
    system_prompt: null,
  },
  {
    id: 'r2',
    policy_id: 'p2',
    policy_name: 'grafana-alert-responder',
    status: 'running',
    trigger_type: 'poll',
    started_at: '2026-03-16T10:10:00Z',
    completed_at: null,
    token_cost: 400,
    error: null,
    created_at: '2026-03-16T10:10:00Z',
    system_prompt: null,
  },
  {
    id: 'r3',
    policy_id: 'p3',
    policy_name: 'deploy-checker',
    status: 'waiting_for_approval',
    trigger_type: 'webhook',
    started_at: '2026-03-16T10:15:00Z',
    completed_at: null,
    token_cost: 800,
    error: null,
    created_at: '2026-03-16T10:15:00Z',
    system_prompt: null,
  },
  {
    id: 'r4',
    policy_id: 'p4',
    policy_name: 'backup-validator',
    status: 'failed',
    trigger_type: 'scheduled',
    started_at: '2026-03-16T09:00:00Z',
    completed_at: '2026-03-16T09:01:00Z',
    token_cost: 100,
    error: 'tool call timed out',
    created_at: '2026-03-16T09:00:00Z',
    system_prompt: null,
  },
]

export const WithActivity: Story = {
  args: { runs: MOCK_RUNS, isLoading: false },
}

export const Empty: Story = {
  args: { runs: [], isLoading: false },
}

export const Loading: Story = {
  args: { runs: [], isLoading: true },
}
