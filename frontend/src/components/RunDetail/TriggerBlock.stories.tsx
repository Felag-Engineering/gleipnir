import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { TriggerBlock } from './TriggerBlock'

const meta: Meta<typeof TriggerBlock> = {
  title: 'RunDetail/TriggerBlock',
  component: TriggerBlock,
}

export default meta
type Story = StoryObj<typeof TriggerBlock>

export const Webhook: Story = {
  args: {
    triggerType: 'webhook',
    payload: JSON.stringify({
      repository: 'gleipnir',
      ref: 'refs/heads/main',
      pusher: { name: 'operator' },
    }),
  },
}

export const Manual: Story = {
  args: {
    triggerType: 'manual',
    payload: '{}',
  },
}

export const Poll: Story = {
  args: {
    triggerType: 'poll',
    payload: JSON.stringify({ condition_met: true, checked_at: '2026-04-08T10:00:00Z' }),
  },
}

// The component falls back to the raw string when JSON.parse throws.
export const InvalidJSON: Story = {
  args: {
    triggerType: 'webhook',
    payload: 'not-json-payload',
  },
}

export const Empty: Story = {
  args: {
    triggerType: 'manual',
    payload: null,
  },
}
