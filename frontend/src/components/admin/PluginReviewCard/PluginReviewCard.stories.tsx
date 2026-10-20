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
  manifest_version: 1,
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
  manifest_version: 1,
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
  manifest_version: 1,
  created_at: '2025-05-03T09:00:00Z',
}

const v2Plugin: ApiPluginDetail = {
  id: 'plugin-notify-v2',
  name: 'Acme Notify',
  version: '2.0.0',
  description: 'Posts notifications and relays operator replies.',
  author: 'Acme',
  license: 'Apache-2.0',
  status: 'pending_review',
  services: [],
  tier2_capabilities: ['run_history_read'],
  auth_strategy: 'oauth2_authcode',
  has_oauth_defaults: true,
  pubkey_fingerprint: 'a1b2c3d4e5f60002',
  has_sbom: true,
  manifest_version: 2,
  row_version: 3,
  created_at: '2026-08-01T10:00:00Z',
  v2: {
    image: {
      identifier:
        'ghcr.io/acme/notify@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef',
      repository: 'ghcr.io/acme/notify',
      digest: 'sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef',
      registry_type: 'oci',
      version: '2.0.0',
      transport_type: 'streamable-http',
      transport_port: 8080,
    },
    egress: [
      { domain: 'api.acme.example', reason: 'Posts messages to the Acme API' },
      { domain: 'files.acme.example', reason: 'Downloads attachments' },
    ],
    resources: { memory_mb: 512 },
    effective_resources: {
      memory_mb: 512,
      cpu_millicores: 500,
      memory_source: 'manifest',
      cpu_source: 'default',
    },
    profiles: {
      declared: ['tool_provider', 'human_channel', 'event_source', 'identity_provider'],
      human_channel_assurance: 'authenticated',
      identity_link_methods: ['inbound_code'],
    },
    tools: [
      { name: 'post_message' },
      { name: 'delete_channel', elicitation_kind: 'permission' },
      { name: 'pick_channel', elicitation_kind: 'information' },
    ],
    event_kinds: [{ kind: 'message_posted', description: 'A message was posted' }],
    auth: {
      strategy: 'oauth2_authcode',
      oauth_defaults: {
        authorization_url: 'https://acme.example/oauth/authorize',
        token_url: 'https://acme.example/oauth/token',
        scopes: ['chat:write'],
      },
    },
    config_schema: { type: 'object', properties: { workspace: { type: 'string' } } },
  },
}

const v2NoEgressPlugin: ApiPluginDetail = {
  ...v2Plugin,
  id: 'plugin-offline-v2',
  name: 'Offline Formatter',
  description: 'Pure computation; never touches the network.',
  tier2_capabilities: [],
  auth_strategy: 'none',
  has_oauth_defaults: false,
  v2: {
    ...v2Plugin.v2!,
    egress: [],
    resources: null,
    effective_resources: {
      memory_mb: 256,
      cpu_millicores: 500,
      memory_source: 'default',
      cpu_source: 'default',
    },
    profiles: { declared: ['tool_provider'] },
    tools: [{ name: 'format_text' }],
    event_kinds: [],
    auth: { strategy: 'none' },
    config_schema: null,
  },
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

// Container (v2) plugin: network access, resource limits with sources,
// capabilities and the pinned image.
export const ContainerPlugin: Story = {
  args: { plugin: v2Plugin },
}

// Container plugin that requests no egress: the Network access section reads
// "No internet access", and limits fall back to the host default.
export const ContainerPluginNoEgress: Story = {
  args: { plugin: v2NoEgressPlugin },
}
