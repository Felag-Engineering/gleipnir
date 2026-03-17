import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { UnassignedBanner } from './UnassignedBanner'

const meta: Meta<typeof UnassignedBanner> = {
  title: 'ToolsPage/UnassignedBanner',
  component: UnassignedBanner,
}

export default meta
type Story = StoryObj<typeof UnassignedBanner>

export const OneUnassigned: Story = { args: { count: 1 } }
export const MultipleUnassigned: Story = { args: { count: 4 } }
export const NoneUnassigned: Story = { args: { count: 0 } }
