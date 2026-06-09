import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { UninstallPluginModal } from './UninstallPluginModal'

const meta: Meta<typeof UninstallPluginModal> = {
  title: 'Admin/UninstallPluginModal',
  component: UninstallPluginModal,
}

export default meta
type Story = StoryObj<typeof UninstallPluginModal>

// Default (Ready) — no instances remain, plugin can be uninstalled immediately.
// Admin sees a confirmation and the enabled "Uninstall plugin" button.
export const Default: Story = {
  args: {
    pluginName: 'my-slack-plugin',
    instanceNames: [],
    onClose: () => {},
    onConfirm: () => {},
    isPending: false,
    error: null,
  },
}

// Blocked — instances must be deleted before the plugin can be uninstalled.
// Submit is disabled and remaining instance names are listed.
export const Blocked: Story = {
  args: {
    pluginName: 'my-slack-plugin',
    instanceNames: ['prod', 'staging'],
    onClose: () => {},
    onConfirm: () => {},
    isPending: false,
    error: null,
  },
}

// Pending — uninstall request is in flight (zero instances); button is disabled.
export const Pending: Story = {
  args: {
    pluginName: 'my-slack-plugin',
    instanceNames: [],
    onClose: () => {},
    onConfirm: () => {},
    isPending: true,
    error: null,
  },
}

// ErrorReferenced — the backend returned an unexpected error.
export const ErrorReferenced: Story = {
  args: {
    pluginName: 'my-slack-plugin',
    instanceNames: [],
    onClose: () => {},
    onConfirm: () => {},
    isPending: false,
    error: 'Server error — please try again.',
  },
}
