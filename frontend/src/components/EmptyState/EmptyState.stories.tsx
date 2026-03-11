import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router-dom'
import EmptyState from './EmptyState'
import '@/tokens.css'

const meta: Meta<typeof EmptyState> = {
  title: 'Shared/EmptyState',
  component: EmptyState,
  decorators: [(Story) => (<MemoryRouter><Story /></MemoryRouter>)],
}

export default meta
type Story = StoryObj<typeof EmptyState>

export const Default: Story = {
  args: {
    headline: 'No policies yet',
    subtext: 'Create your first policy to start running agents',
    ctaLabel: 'Create policy',
    ctaTo: '/policies/new',
  },
}

export const CustomMessage: Story = {
  args: {
    headline: 'No runs yet',
    subtext: 'Trigger a webhook or enable a cron policy to see runs here',
    ctaLabel: 'Go to policies',
    ctaTo: '/dashboard',
  },
}
