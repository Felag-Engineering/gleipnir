import type { Meta, StoryObj } from '@storybook/react-vite'
import { userEvent, within } from 'storybook/test'
import '@/tokens.css'
import { ToastProvider, useToast } from './ToastProvider'
import { ToastRegion } from './ToastRegion'

function ToastTriggers() {
  const toast = useToast()
  return (
    <div style={{ display: 'flex', gap: 8 }}>
      <button onClick={() => toast.success('Agent saved')}>Fire success</button>
      <button onClick={() => toast.error("Couldn't save agent")}>Fire error</button>
      <button onClick={() => toast.info('Heads up')}>Fire info</button>
    </div>
  )
}

const meta: Meta<typeof ToastRegion> = {
  title: 'Shared/Toast',
  component: ToastRegion,
  decorators: [
    (Story) => (
      <ToastProvider>
        <ToastTriggers />
        <Story />
      </ToastProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof ToastRegion>

export const Default: Story = {}

// Drives the trigger buttons through real userEvent interactions to show the
// success/error/info variants stacked together.
export const StackedToasts: Story = {
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.click(canvas.getByText('Fire success'))
    await userEvent.click(canvas.getByText('Fire error'))
    await userEvent.click(canvas.getByText('Fire info'))
  },
}
