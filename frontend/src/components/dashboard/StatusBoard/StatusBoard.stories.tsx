import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router-dom'
import '@/tokens.css'
import { StatusBoard } from './StatusBoard'
import type { ApiPolicyListItem } from '@/api/types'

const meta: Meta<typeof StatusBoard> = {
  title: 'Dashboard/StatusBoard',
  component: StatusBoard,
  decorators: [
    (Story) => (
      <MemoryRouter>
        <Story />
      </MemoryRouter>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof StatusBoard>

const base: Omit<ApiPolicyListItem, 'id' | 'name' | 'latest_run'> = {
  trigger_type: 'webhook',
  folder: '',
  created_at: '2026-03-16T08:00:00Z',
  updated_at: '2026-03-16T08:00:00Z',
  paused_at: null,
}

const MIXED: ApiPolicyListItem[] = [
  {
    ...base,
    id: 'p1',
    name: 'vikunja-triage',
    latest_run: { id: 'r1', status: 'complete', started_at: '2026-03-16T10:00:00Z', token_cost: 1200 },
  },
  {
    ...base,
    id: 'p2',
    name: 'grafana-alert-responder',
    trigger_type: 'poll',
    latest_run: { id: 'r2', status: 'running', started_at: '2026-03-16T10:10:00Z', token_cost: 400 },
  },
  {
    ...base,
    id: 'p3',
    name: 'deploy-checker',
    latest_run: { id: 'r3', status: 'waiting_for_approval', started_at: '2026-03-16T10:15:00Z', token_cost: 800 },
  },
  {
    ...base,
    id: 'p4',
    name: 'backup-validator',
    trigger_type: 'scheduled',
    latest_run: { id: 'r4', status: 'failed', started_at: '2026-03-16T09:00:00Z', token_cost: 100 },
  },
]

export const WithMixedStates: Story = {
  args: { policies: MIXED, onTrigger: () => {} },
}

export const AllIdle: Story = {
  args: {
    policies: MIXED.map(p => ({ ...p, latest_run: null })),
    onTrigger: () => {},
  },
}

export const WithApprovals: Story = {
  args: {
    policies: MIXED.map(p => ({
      ...p,
      latest_run: { id: `r-${p.id}`, status: 'waiting_for_approval', started_at: '2026-03-16T10:15:00Z', token_cost: 0 },
    })),
    onTrigger: () => {},
  },
}
