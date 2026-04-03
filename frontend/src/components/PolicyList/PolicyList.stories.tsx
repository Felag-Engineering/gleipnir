import type { Meta, StoryObj } from '@storybook/react-vite'
import { fn } from 'storybook/test'
import { MemoryRouter } from 'react-router-dom'
import '@/tokens.css'
import type { ApiPolicyListItem } from '@/api/types'
import { PolicyList } from './PolicyList'

const meta: Meta<typeof PolicyList> = {
  title: 'Components/PolicyList',
  component: PolicyList,
  decorators: [(Story) => (<MemoryRouter><Story /></MemoryRouter>)],
  args: {
    onTrigger: fn(),
  },
}

export default meta
type Story = StoryObj<typeof PolicyList>

const FIXTURE_POLICIES: ApiPolicyListItem[] = [
  {
    id: 'pol-1',
    name: 'Nightly Backup Check',
    trigger_type: 'cron',
    folder: 'Infrastructure',
    model: 'claude-opus-4-5',
    tool_count: 3,
    created_at: '2026-03-01T00:00:00Z',
    updated_at: '2026-03-09T00:00:00Z',
    paused_at: null,
    latest_run: {
      id: 'run-1',
      status: 'complete',
      started_at: '2026-03-10T02:00:00Z',
      token_cost: 2340,
    },
    avg_token_cost: 2340,
  },
  {
    id: 'pol-2',
    name: 'Deploy Agent',
    trigger_type: 'webhook',
    folder: 'CI/CD',
    model: 'claude-opus-4-5',
    tool_count: 5,
    created_at: '2026-02-15T00:00:00Z',
    updated_at: '2026-03-10T00:00:00Z',
    paused_at: null,
    latest_run: {
      id: 'run-2',
      status: 'running',
      started_at: '2026-03-10T12:45:00Z',
      token_cost: 870,
    },
    avg_token_cost: 870,
  },
  {
    id: 'pol-3',
    name: 'DB Cleanup',
    trigger_type: 'cron',
    folder: 'Infrastructure',
    model: '',
    tool_count: 2,
    created_at: '2026-02-20T00:00:00Z',
    updated_at: '2026-03-08T00:00:00Z',
    paused_at: null,
    latest_run: {
      id: 'run-3',
      status: 'failed',
      started_at: '2026-03-10T01:00:00Z',
      token_cost: 1100,
    },
    avg_token_cost: 1100,
  },
  {
    id: 'pol-4',
    name: 'Approval Gate',
    trigger_type: 'webhook',
    folder: 'Security',
    model: '',
    tool_count: 1,
    created_at: '2026-03-05T00:00:00Z',
    updated_at: '2026-03-09T00:00:00Z',
    paused_at: null,
    latest_run: {
      id: 'run-4',
      status: 'waiting_for_approval',
      started_at: '2026-03-10T11:30:00Z',
      token_cost: 560,
    },
    avg_token_cost: 560,
  },
  {
    id: 'pol-5',
    name: 'Poll Monitor',
    trigger_type: 'poll',
    folder: 'Monitoring',
    model: '',
    tool_count: 0,
    created_at: '2026-03-02T00:00:00Z',
    updated_at: '2026-03-07T00:00:00Z',
    paused_at: null,
    latest_run: {
      id: 'run-5',
      status: 'interrupted',
      started_at: '2026-03-09T23:15:00Z',
      token_cost: 430,
    },
    avg_token_cost: 430,
  },
  {
    id: 'pol-6',
    name: 'Pre-flight Check',
    trigger_type: 'webhook',
    folder: 'CI/CD',
    model: '',
    tool_count: 0,
    created_at: '2026-03-08T00:00:00Z',
    updated_at: '2026-03-10T00:00:00Z',
    paused_at: null,
    latest_run: {
      id: 'run-6',
      status: 'pending',
      started_at: '2026-03-10T12:50:00Z',
      token_cost: 0,
    },
    avg_token_cost: 0,
  },
  {
    id: 'pol-7',
    name: 'Unused Cron Job',
    trigger_type: 'cron',
    folder: '',
    model: '',
    tool_count: 0,
    created_at: '2026-01-10T00:00:00Z',
    updated_at: '2026-01-10T00:00:00Z',
    paused_at: null,
    latest_run: null,
    avg_token_cost: 0,
  },
  {
    id: 'pol-8',
    name: 'Manual Healthcheck',
    trigger_type: 'manual',
    folder: 'Infrastructure',
    model: 'claude-opus-4-5',
    tool_count: 4,
    created_at: '2026-03-10T00:00:00Z',
    updated_at: '2026-03-10T00:00:00Z',
    paused_at: null,
    latest_run: {
      id: 'run-8',
      status: 'complete',
      started_at: '2026-03-10T14:00:00Z',
      token_cost: 980,
    },
    avg_token_cost: 980,
  },
]

export const Flat: Story = {
  args: {
    policies: FIXTURE_POLICIES,
    groupByFolder: false,
  },
}

export const GroupedByFolder: Story = {
  args: {
    policies: FIXTURE_POLICIES,
    groupByFolder: true,
  },
}

export const NoTriggerButton: Story = {
  args: {
    policies: FIXTURE_POLICIES,
    onTrigger: undefined,
  },
}

export const Empty: Story = {
  args: {
    policies: [],
  },
}
