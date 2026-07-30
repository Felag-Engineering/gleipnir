import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router'
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
    headline: 'No agents yet',
    subtext: 'Create your first agent to start running',
    ctaLabel: 'Create agent',
    ctaTo: '/agents/new',
  },
}

export const CustomMessage: Story = {
  args: {
    headline: 'No runs yet',
    subtext: 'Trigger a webhook or enable a scheduled agent to see runs here',
    ctaLabel: 'Go to Agents',
    ctaTo: '/dashboard',
  },
}

// Button CTA — used by pages that open a modal instead of navigating.
export const ButtonCta: Story = {
  args: {
    headline: 'No users',
    subtext: 'Create a user to get started.',
    ctaLabel: 'Create user',
    onCtaClick: () => {},
  },
}

// No CTA — e.g. a read-only viewer with nothing to add.
export const NoCta: Story = {
  args: {
    headline: 'No audiences',
    subtext: 'No audiences have been created yet.',
  },
}
