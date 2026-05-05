import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { PluginHealthChip } from './PluginHealthChip'

const meta: Meta<typeof PluginHealthChip> = {
  title: 'Admin/PluginHealthChip',
  component: PluginHealthChip,
  argTypes: {
    state: {
      control: 'select',
      options: [
        'healthy',
        'unsigned_permissive',
        'pending_key_approval',
        'pending_manifest_approval',
        'pending_config_migration',
        'unhealthy',
        'circuit_broken',
        'verification_error',
        'signature_invalid',
        'crashed',
      ],
    },
  },
}

export default meta
type Story = StoryObj<typeof PluginHealthChip>

export const Healthy: Story = { args: { state: 'healthy' } }
export const UnsignedPermissive: Story = { args: { state: 'unsigned_permissive' } }
export const PendingKeyApproval: Story = { args: { state: 'pending_key_approval' } }
export const PendingManifestApproval: Story = { args: { state: 'pending_manifest_approval' } }
export const PendingConfigMigration: Story = { args: { state: 'pending_config_migration' } }
export const Unhealthy: Story = { args: { state: 'unhealthy' } }
export const CircuitBroken: Story = { args: { state: 'circuit_broken' } }
export const VerificationError: Story = { args: { state: 'verification_error' } }
export const SignatureInvalid: Story = { args: { state: 'signature_invalid' } }
export const Crashed: Story = { args: { state: 'crashed' } }

export const WithDetail: Story = {
  args: {
    state: 'verification_error',
    detail: 'Manifest hash does not match trusted snapshot',
    onClick: () => alert('Chip clicked'),
  },
}

// AggregateWorst shows a row of all states to validate color mapping at a glance.
// This is the pattern a future /admin/plugins page would use to show instance health.
export const AggregateWorst: Story = {
  render: () => (
    <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
      <PluginHealthChip state="healthy" />
      <PluginHealthChip state="unsigned_permissive" />
      <PluginHealthChip state="pending_key_approval" />
      <PluginHealthChip state="pending_manifest_approval" />
      <PluginHealthChip state="pending_config_migration" />
      <PluginHealthChip state="unhealthy" />
      <PluginHealthChip state="circuit_broken" />
      <PluginHealthChip state="verification_error" />
      <PluginHealthChip state="signature_invalid" />
      <PluginHealthChip state="crashed" />
    </div>
  ),
}
