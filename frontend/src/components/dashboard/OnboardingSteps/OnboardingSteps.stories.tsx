import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router-dom'
import '@/tokens.css'
import { OnboardingSteps } from './OnboardingSteps'

const meta: Meta<typeof OnboardingSteps> = {
  title: 'Dashboard/OnboardingSteps',
  component: OnboardingSteps,
  decorators: [
    (Story) => (
      <MemoryRouter>
        <Story />
      </MemoryRouter>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof OnboardingSteps>

export const AllIncomplete: Story = {
  args: { hasServers: false, hasPolicies: false, hasRuns: false },
}

export const ServerAdded: Story = {
  args: { hasServers: true, hasPolicies: false, hasRuns: false },
}

export const PolicyCreated: Story = {
  args: { hasServers: true, hasPolicies: true, hasRuns: false },
}

export const AllComplete: Story = {
  args: { hasServers: true, hasPolicies: true, hasRuns: true },
}
