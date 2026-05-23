import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { UninstallPluginModal } from './UninstallPluginModal'

const meta: Meta<typeof UninstallPluginModal> = {
  title: 'Admin/UninstallPluginModal',
  component: UninstallPluginModal,
}

export default meta
type Story = StoryObj<typeof UninstallPluginModal>

// Default — idle state. Admin sees the plugin name, the instance list, and a
// description of what will be removed.
export const Default: Story = {
  args: {
    pluginName: 'my-slack-plugin',
    instanceNames: ['prod', 'staging'],
    onClose: () => {},
    onConfirm: () => {},
    isPending: false,
    error: null,
  },
}

// Pending — uninstall request is in flight; button is disabled.
export const Pending: Story = {
  args: {
    pluginName: 'my-slack-plugin',
    instanceNames: ['prod'],
    onClose: () => {},
    onConfirm: () => {},
    isPending: true,
    error: null,
  },
}

// ErrorReferenced — the backend returned 409 because a policy or audience
// still references one of the plugin's instances.
export const ErrorReferenced: Story = {
  args: {
    pluginName: 'my-slack-plugin',
    instanceNames: ['prod', 'staging'],
    onClose: () => {},
    onConfirm: () => {},
    isPending: false,
    error: 'Policy "Nightly Sync" references instance prod. Remove all policy tool references before uninstalling.',
  },
}

// NoInstances — plugin was installed but no instances were created yet.
export const NoInstances: Story = {
  args: {
    pluginName: 'orphan-plugin',
    instanceNames: [],
    onClose: () => {},
    onConfirm: () => {},
    isPending: false,
    error: null,
  },
}
