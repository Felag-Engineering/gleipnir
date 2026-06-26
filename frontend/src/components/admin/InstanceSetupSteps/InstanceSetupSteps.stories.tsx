import type { Meta, StoryObj } from '@storybook/react-vite'
import { action } from 'storybook/actions'
import '@/tokens.css'
import { InstanceSetupSteps } from './InstanceSetupSteps'
import { deriveSetupSteps, humanizeHealthDetail } from '@/utils/instanceSetup'

const meta: Meta<typeof InstanceSetupSteps> = {
  title: 'Admin/InstanceSetupSteps',
  component: InstanceSetupSteps,
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 640, padding: 24 }}>
        <Story />
      </div>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof InstanceSetupSteps>

// A Slack-shaped instance: OAuth credentials, one required config field
// (app-level token), and a subscription scope.
const fullInput = {
  authStrategy: 'oauth2_authcode',
  configSchema: { required: ['app_level_token'] },
  hasSubscriptionSchema: true,
}

export const AllIncomplete: Story = {
  args: {
    steps: deriveSetupSteps({
      ...fullInput,
      credentials: undefined,
      config: {},
      subscriptionScope: {},
    }),
    healthDetail: humanizeHealthDetail('config_missing'),
    onNavigate: action('navigate'),
  },
}

export const CredentialsDone: Story = {
  args: {
    steps: deriveSetupSteps({
      ...fullInput,
      credentials: { strategy: 'oauth2_authcode', has_token: true },
      config: {},
      subscriptionScope: {},
    }),
    healthDetail: humanizeHealthDetail('credentials_missing'),
    onNavigate: action('navigate'),
  },
}

export const ConfigDoneScopeLeft: Story = {
  args: {
    steps: deriveSetupSteps({
      ...fullInput,
      credentials: { strategy: 'oauth2_authcode', has_token: true },
      config: { app_level_token: '***' },
      subscriptionScope: {},
    }),
    healthDetail: null,
    onNavigate: action('navigate'),
  },
}

export const AllDone: Story = {
  args: {
    steps: deriveSetupSteps({
      ...fullInput,
      credentials: { strategy: 'oauth2_authcode', has_token: true },
      config: { app_level_token: '***' },
      subscriptionScope: { channels: ['C123'] },
    }),
    healthDetail: null,
    onNavigate: action('navigate'),
  },
}

// A "none"-strategy, config-only plugin: just the required config step.
export const ConfigOnly: Story = {
  args: {
    steps: deriveSetupSteps({
      authStrategy: 'none',
      configSchema: { required: ['endpoint_url'] },
      config: {},
      hasSubscriptionSchema: false,
    }),
    healthDetail: humanizeHealthDetail('config_missing'),
    onNavigate: action('navigate'),
  },
}
