import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import { ReauthorizeButton } from './ReauthorizeButton'

const meta: Meta<typeof ReauthorizeButton> = {
  title: 'Admin/ReauthorizeButton',
  component: ReauthorizeButton,
  decorators: [
    (Story) => (
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <div style={{ padding: 16 }}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
  args: {
    pluginId: 'plugin-slack-01',
    instanceId: 'inst-slack-prod',
  },
}

export default meta
type Story = StoryObj<typeof ReauthorizeButton>

// For oauth2_authcode: clicking will attempt to navigate to an authorize URL.
export const AuthcodeStrategy: Story = {
  args: { strategy: 'oauth2_authcode' },
}

// For oauth2_clientcred: clicking performs the token exchange synchronously
// (no browser redirect).
export const ClientcredStrategy: Story = {
  args: { strategy: 'oauth2_clientcred' },
}

// Non-OAuth strategies render nothing — the component is a no-op.
export const NonOAuthStrategyRendersNothing: Story = {
  args: { strategy: 'static_api_key' },
}

// Pending state — the button is disabled and shows "Starting…".
// Note: Storybook can't freeze mutation.isPending=true without MSW; the
// unit test in ReauthorizeButton.test.tsx validates this behavior directly.
export const PendingState: Story = {
  args: { strategy: 'oauth2_authcode' },
  name: 'Pending state (label "Starting…")',
}

// Error state — the mutation has failed; the button is re-enabled for retry.
export const ErrorState: Story = {
  args: { strategy: 'oauth2_authcode' },
  name: 'Error state (button re-enabled after failure)',
}
