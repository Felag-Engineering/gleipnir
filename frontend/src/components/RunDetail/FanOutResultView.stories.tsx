import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ALL_OK_24, EMPTY, MIXED_24, SINGLE } from './fanOutFixtures'
import { FanOutResultView } from './FanOutResultView'

const meta: Meta<typeof FanOutResultView> = {
  title: 'RunDetail/FanOutResultView',
  component: FanOutResultView,
}

export default meta
type Story = StoryObj<typeof FanOutResultView>

export const AllOk: Story = {
  args: { result: ALL_OK_24 },
}

export const MixedOutcomes: Story = {
  args: { result: MIXED_24 },
}

export const SingleNode: Story = {
  args: { result: SINGLE },
}

export const Empty: Story = {
  args: { result: EMPTY },
}
