import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import SkeletonList from './SkeletonList'

const meta: Meta<typeof SkeletonList> = {
  title: 'Components/SkeletonList',
  component: SkeletonList,
}

export default meta
type Story = StoryObj<typeof SkeletonList>

export const Default: Story = {
  args: {
    count: 5,
    height: 48,
  },
}

export const Short: Story = {
  args: {
    count: 2,
    height: 48,
  },
}

export const Long: Story = {
  args: {
    count: 10,
    height: 48,
  },
}

export const TallRows: Story = {
  args: {
    count: 4,
    height: 80,
  },
}
