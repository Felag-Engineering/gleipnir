import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { PluginReviewCard } from './PluginReviewCard'
import type { ApiPluginDetail } from '@/api/types'

const slackPlugin: ApiPluginDetail = {
  id: 'plugin-slack-01',
  name: 'Slack',
  version: '1.2.0',
  description: 'Sends messages, handles approvals, and emits events from Slack channels.',
  author: 'Gleipnir Labs',
  license: 'MIT',
  status: 'pending_review',
  services: ['tool', 'trigger', 'channel'],
  tier2_capabilities: ['run_history_read'],
  auth_strategy: 'oauth2_authcode',
  has_oauth_defaults: true,
  pubkey_fingerprint: 'a1b2c3d4e5f60001',
  has_sbom: true,
  created_at: '2025-05-01T10:00:00Z',
}

const githubPlugin: ApiPluginDetail = {
  id: 'plugin-github-01',
  name: 'GitHub',
  version: '0.9.1',
  description: 'Provides tools for querying and mutating GitHub repositories.',
  status: 'pending_review',
  services: ['tool'],
  auth_strategy: 'static_api_key',
  has_oauth_defaults: false,
  pubkey_fingerprint: 'deadbeef00000001',
  has_sbom: false,
  created_at: '2025-05-02T11:00:00Z',
}

const minimalPlugin: ApiPluginDetail = {
  id: 'plugin-min-01',
  name: 'minimal-plugin',
  version: '0.1.0',
  status: 'pending_review',
  services: ['tool'],
  auth_strategy: 'none',
  has_oauth_defaults: false,
  has_sbom: false,
  created_at: '2025-05-03T09:00:00Z',
}

const meta: Meta<typeof PluginReviewCard> = {
  title: 'Admin/PluginReviewCard',
  component: PluginReviewCard,
  parameters: {
    layout: 'padded',
  },
  args: {
    onApprove: () => {},
    onReject: () => {},
    isApproving: false,
    isRejecting: false,
  },
}

export default meta
type Story = StoryObj<typeof PluginReviewCard>

// Full metadata: all services, OAuth, SBOM, tier-2 capabilities.
export const FullMetadata: Story = {
  args: { plugin: slackPlugin },
}

// Tool-only, static API key, no SBOM.
export const ToolOnlyNoSBOM: Story = {
  args: { plugin: githubPlugin },
}

// Minimal plugin: no description, no author, no license, no SBOM.
export const Minimal: Story = {
  args: { plugin: minimalPlugin },
}

// Approve button shows spinner text while the mutation is in-flight.
export const ApprovePending: Story = {
  args: { plugin: slackPlugin, isApproving: true },
}

// Reject button shows spinner text while the mutation is in-flight.
export const RejectPending: Story = {
  args: { plugin: slackPlugin, isRejecting: true },
}
