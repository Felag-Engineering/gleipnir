import type { Meta, StoryObj } from '@storybook/react-vite'
import ApprovalBanner from './ApprovalBanner'
import '../../tokens.css'

const meta: Meta<typeof ApprovalBanner> = {
  title: 'Shared/ApprovalBanner',
  component: ApprovalBanner,
  argTypes: {
    count: { control: 'number' },
  },
}

export default meta
type Story = StoryObj<typeof ApprovalBanner>

export const Hidden: Story = {
  args: { count: 0 },
}

export const WithApprovals: Story = {
  args: { count: 3 },
}
