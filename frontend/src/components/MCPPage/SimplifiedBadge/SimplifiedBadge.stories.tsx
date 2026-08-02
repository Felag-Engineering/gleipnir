import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { SimplifiedBadge } from './SimplifiedBadge'

const meta: Meta<typeof SimplifiedBadge> = {
  title: 'ToolsPage/SimplifiedBadge',
  component: SimplifiedBadge,
}

export default meta
type Story = StoryObj<typeof SimplifiedBadge>

export const OneProvider: Story = {
  args: { providers: ['google'] },
}

export const TwoProviders: Story = {
  args: { providers: ['google', 'openai'] },
}

export const ThreeProviders: Story = {
  args: { providers: ['google', 'openai', 'mistral'] },
}
