import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ALL_OK_24, ANSWER_REFUSED, APPROVED_RETRY, DEMO_HOSTNAMES, DEMO_MIXED, DENIED_RETRY, EMPTY, MIXED_24, SINGLE } from './fanOutFixtures'
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

export const ApprovedRetry: Story = {
  args: { result: APPROVED_RETRY },
}

// Hostnames come from a list_nodes result earlier in the same run; each row
// keeps its full node_id beneath the hostname.
export const ApprovedRetryWithHostnames: Story = {
  args: { result: APPROVED_RETRY, hostnames: DEMO_HOSTNAMES },
}

export const MixedWithHostnames: Story = {
  args: { result: DEMO_MIXED, hostnames: DEMO_HOSTNAMES },
}

// Only some ids are known: the rest fall back to their node_id.
export const PartialHostnames: Story = {
  args: { result: DEMO_MIXED, hostnames: new Map([['node-814e5b14eca5206060abdb164672609f', 'dev-node-4']]) },
}

export const RejectedApproval: Story = {
  args: { result: DENIED_RETRY },
}

export const AnswerRefused: Story = {
  args: { result: ANSWER_REFUSED },
}
