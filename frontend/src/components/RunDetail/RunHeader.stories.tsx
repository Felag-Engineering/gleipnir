import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router-dom'
import '@/tokens.css'
import type { ApiRun } from '@/api/types'
import { RunHeader } from './RunHeader'

const meta: Meta<typeof RunHeader> = {
  title: 'RunDetail/RunHeader',
  component: RunHeader,
  decorators: [(Story) => <MemoryRouter><Story /></MemoryRouter>],
}

export default meta
type Story = StoryObj<typeof RunHeader>

const BASE_RUN: ApiRun = {
  id: 'run-abc123',
  policy_id: 'pol-1',
  policy_name: 'Nightly Backup Check',
  status: 'complete',
  trigger_type: 'cron',
  trigger_payload: '{}',
  started_at: '2026-03-10T02:00:00Z',
  completed_at: '2026-03-10T02:03:45Z',
  token_cost: 2340,
  error: null,
  created_at: '2026-03-10T02:00:00Z',
  system_prompt: null,
}

export const Complete: Story = {
  args: {
    run: BASE_RUN,
    toolCallCount: 7,
    tokenTotal: 2340,
    duration: 225_000,
  },
}

export const Running: Story = {
  args: {
    run: { ...BASE_RUN, status: 'running', completed_at: null },
    toolCallCount: 3,
    tokenTotal: 890,
    duration: null,
  },
}

export const Failed: Story = {
  args: {
    run: { ...BASE_RUN, status: 'failed', error: 'MCP server unreachable after 3 retries' },
    toolCallCount: 5,
    tokenTotal: 1800,
    duration: 120_000,
  },
}

export const WaitingForApproval: Story = {
  args: {
    run: { ...BASE_RUN, status: 'waiting_for_approval', trigger_type: 'webhook' },
    toolCallCount: 2,
    tokenTotal: 450,
    duration: null,
  },
}

export const WebhookTrigger: Story = {
  args: {
    run: { ...BASE_RUN, trigger_type: 'webhook', status: 'complete' },
    toolCallCount: 7,
    tokenTotal: 2340,
    duration: 225_000,
  },
}

export const DeletedPolicy: Story = {
  name: 'Deleted policy (falls back to policy_id)',
  args: {
    run: { ...BASE_RUN, policy_name: '' },
    toolCallCount: 7,
    tokenTotal: 2340,
    duration: 225_000,
  },
}
