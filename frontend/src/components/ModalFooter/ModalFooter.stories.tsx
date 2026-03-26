import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ModalFooter } from './ModalFooter'

const meta: Meta<typeof ModalFooter> = {
  title: 'Components/ModalFooter',
  component: ModalFooter,
  argTypes: {
    variant: { control: 'select', options: ['primary', 'danger'] },
    isLoading: { control: 'boolean' },
    submitDisabled: { control: 'boolean' },
  },
  decorators: [
    (Story) => (
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', padding: 16 }}>
        <Story />
      </div>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof ModalFooter>

export const Primary: Story = {
  args: {
    onCancel: () => {},
    isLoading: false,
    submitLabel: 'Add MCP server',
  },
}

export const Danger: Story = {
  args: {
    onCancel: () => {},
    isLoading: false,
    submitLabel: 'Delete MCP server',
    variant: 'danger',
  },
}

export const Loading: Story = {
  args: {
    onCancel: () => {},
    isLoading: true,
    submitLabel: 'Add MCP server',
    loadingLabel: 'Adding…',
  },
}

export const LoadingDanger: Story = {
  args: {
    onCancel: () => {},
    isLoading: true,
    submitLabel: 'Delete MCP server',
    loadingLabel: 'Deleting…',
    variant: 'danger',
  },
}

export const SubmitDisabled: Story = {
  args: {
    onCancel: () => {},
    isLoading: false,
    submitLabel: 'Add MCP server',
    submitDisabled: true,
  },
}
