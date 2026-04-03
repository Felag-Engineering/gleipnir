import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ParamChip } from './ParamChip'

const meta: Meta<typeof ParamChip> = {
  title: 'ToolsPage/ParamChip',
  component: ParamChip,
}

export default meta
type Story = StoryObj<typeof ParamChip>

export const Required: Story = {
  args: { name: 'message', type: 'string', required: true },
}

export const Optional: Story = {
  args: { name: 'limit', type: 'integer', required: false },
}
