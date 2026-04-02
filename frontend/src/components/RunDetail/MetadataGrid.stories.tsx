import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import type { ApiRun } from '@/api/types'
import { MetadataGrid } from './MetadataGrid'

const meta: Meta<typeof MetadataGrid> = {
  title: 'RunDetail/MetadataGrid',
  component: MetadataGrid,
}

export default meta
type Story = StoryObj<typeof MetadataGrid>

const BASE_RUN: ApiRun = {
  id: 'run-abc123def456',
  policy_id: 'pol-1',
  policy_name: 'Nightly Backup Check',
  status: 'complete',
  trigger_type: 'cron',
  started_at: '2026-03-10T02:00:00Z',
  completed_at: '2026-03-10T02:03:45Z',
  token_cost: 2340,
  error: null,
  created_at: '2026-03-10T02:00:00Z',
  system_prompt: null,
  model: 'claude-sonnet-4-6',
}

export const Default: Story = {
  args: {
    run: BASE_RUN,
    toolCallCount: 7,
    tokenTotal: 2340,
    duration: 225_000,
  },
}

export const LargeTokenCount: Story = {
  args: {
    run: BASE_RUN,
    toolCallCount: 42,
    tokenTotal: 1_480_000,
    duration: 18 * 60 * 1000,
  },
}

export const StillRunning: Story = {
  name: 'Still running (no duration)',
  args: {
    run: { ...BASE_RUN, status: 'running', completed_at: null },
    toolCallCount: 3,
    tokenTotal: 890,
    duration: null,
  },
}

export const NoToolCalls: Story = {
  args: {
    run: BASE_RUN,
    toolCallCount: 0,
    tokenTotal: 150,
    duration: 8_000,
  },
}
