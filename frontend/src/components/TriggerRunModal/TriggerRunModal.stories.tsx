import type { Meta, StoryObj } from '@storybook/react-vite'
import { fn } from 'storybook/test'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import { TriggerRunModal } from './TriggerRunModal'

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false } },
})

const meta: Meta<typeof TriggerRunModal> = {
  title: 'Components/TriggerRunModal',
  component: TriggerRunModal,
  decorators: [
    (Story) => (
      <QueryClientProvider client={queryClient}>
        <Story />
      </QueryClientProvider>
    ),
  ],
  args: {
    policyId: 'pol-example',
    policyName: 'Manual Healthcheck',
    onClose: fn(),
    onSuccess: fn(),
  },
}

export default meta
type Story = StoryObj<typeof TriggerRunModal>

export const Default: Story = {}

export const WithLongName: Story = {
  args: {
    policyName: 'Deploy to Production Environment Agent',
  },
}

export const WithPrefill: Story = {
  args: {
    initialMessage: 'Check the logs on the production server for errors in the last hour.',
    policyUpdatedAt: new Date(Date.now() - 2 * 60 * 60 * 1000).toISOString(),
  },
}
