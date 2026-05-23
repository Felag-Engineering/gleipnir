import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { DeletePluginInstanceModal } from './DeletePluginInstanceModal'

const meta: Meta<typeof DeletePluginInstanceModal> = {
  title: 'Admin/DeletePluginInstanceModal',
  component: DeletePluginInstanceModal,
}

export default meta
type Story = StoryObj<typeof DeletePluginInstanceModal>

// Default — idle state, no error. Admin sees the plugin + instance names and
// can confirm or cancel.
export const Default: Story = {
  args: {
    pluginName: 'my-slack-plugin',
    instanceName: 'prod',
    onClose: () => {},
    onConfirm: () => {},
    isPending: false,
    error: null,
  },
}

// Pending — delete request is in flight; button is disabled and shows loading text.
export const Pending: Story = {
  args: {
    pluginName: 'my-slack-plugin',
    instanceName: 'staging',
    onClose: () => {},
    onConfirm: () => {},
    isPending: true,
    error: null,
  },
}

// ErrorReferenced — the backend returned 409 because a policy still references
// this instance. The error detail is displayed under the modal body.
export const ErrorReferenced: Story = {
  args: {
    pluginName: 'my-slack-plugin',
    instanceName: 'prod',
    onClose: () => {},
    onConfirm: () => {},
    isPending: false,
    error: 'Policy "Nightly Sync" still references this instance. Remove the policy tool reference first.',
  },
}
