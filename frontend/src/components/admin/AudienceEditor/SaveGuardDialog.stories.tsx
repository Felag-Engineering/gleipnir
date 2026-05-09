import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router-dom'
import '@/tokens.css'
import { SaveGuardDialog } from './SaveGuardDialog'
import type { ApiAudienceReferences } from '@/api/types'

const meta: Meta<typeof SaveGuardDialog> = {
  title: 'Admin/AudienceEditor/SaveGuardDialog',
  component: SaveGuardDialog,
  decorators: [
    (Story) => (
      <MemoryRouter>
        <Story />
      </MemoryRouter>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof SaveGuardDialog>

const referencesNoInFlight: ApiAudienceReferences = {
  policies: [
    { id: 'p1', name: 'deploy-bot' },
    { id: 'p2', name: 'smoke-tests' },
  ],
  in_flight_runs: [],
}

const referencesWithInFlight: ApiAudienceReferences = {
  policies: [
    { id: 'p1', name: 'deploy-bot' },
    { id: 'p2', name: 'smoke-tests' },
  ],
  in_flight_runs: [
    { id: 'run-abc123', policy_id: 'p1', status: 'running' },
    { id: 'run-def456', policy_id: 'p2', status: 'waiting_for_approval' },
  ],
}

export const NoInFlight: Story = {
  args: {
    references: referencesNoInFlight,
    onConfirm: () => {},
    onCancel: () => {},
    isPending: false,
  },
}

export const WithInFlight: Story = {
  args: {
    references: referencesWithInFlight,
    onConfirm: () => {},
    onCancel: () => {},
    isPending: false,
  },
}

export const Saving: Story = {
  args: {
    references: referencesWithInFlight,
    onConfirm: () => {},
    onCancel: () => {},
    isPending: true,
  },
}
