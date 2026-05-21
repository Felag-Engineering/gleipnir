import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { http, HttpResponse } from 'msw'
import { userEvent, within } from 'storybook/test'
import '@/tokens.css'
import { InstallPluginButton } from './InstallPluginButton'

function makeQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
}

function Wrapper({ children }: { children: React.ReactNode }) {
  return (
    <QueryClientProvider client={makeQueryClient()}>
      <MemoryRouter>
        <div style={{ padding: '24px', display: 'flex', justifyContent: 'flex-end' }}>
          {children}
        </div>
      </MemoryRouter>
    </QueryClientProvider>
  )
}

const meta: Meta<typeof InstallPluginButton> = {
  title: 'Admin/InstallPluginButton',
  component: InstallPluginButton,
  decorators: [
    (Story) => (
      <Wrapper>
        <Story />
      </Wrapper>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof InstallPluginButton>

// Default — idle button ready to open the OS file picker.
export const Default: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins', () =>
          HttpResponse.json({
            data: { id: 'plugin-slack-01', name: 'Slack', version: '1.2.0', status: 'active' },
          }),
        ),
      ],
    },
  },
  args: {
    onInstalled: () => {},
    hasInstancesForPlugin: () => false,
  },
}

// Uploading — the install endpoint never resolves, leaving the component in the
// uploading state (disabled button + spinner). The play function uploads a file
// but does not wait for completion — we capture the in-progress state.
export const Uploading: Story = {
  parameters: {
    msw: {
      handlers: [
        // Never resolve so the component stays stuck in uploading state.
        http.post('/api/v1/admin/plugins', () => new Promise(() => {})),
      ],
    },
  },
  args: {
    onInstalled: () => {},
    hasInstancesForPlugin: () => false,
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const input = canvas.getByTestId('install-plugin-input')
    // Upload a file but don't await the response — the handler never resolves,
    // so the component stays in uploading state showing the spinner.
    await userEvent.upload(input, new File(['tarball-content'], 'slack.tar.gz', { type: 'application/gzip' }))
  },
}

// Success — drives the component into the success state using a play function
// that uploads a file via the hidden input. The MSW handler returns immediately,
// and the play function waits for the success card to appear.
export const Success: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins', () =>
          HttpResponse.json({
            data: { id: 'plugin-slack-01', name: 'Slack', version: '1.2.0', status: 'active' },
          }),
        ),
        http.post('/api/v1/admin/plugins/:id/instances', () =>
          HttpResponse.json({
            data: {
              id: 'inst-01',
              plugin_id: 'plugin-slack-01',
              instance_name: 'production',
              health_state: 'healthy',
              version: 1,
              created_at: new Date().toISOString(),
              updated_at: new Date().toISOString(),
            },
          }),
        ),
      ],
    },
  },
  args: {
    onInstalled: () => {},
    // Returns false so the success card stays visible (plugin has no instances yet).
    hasInstancesForPlugin: () => false,
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const input = canvas.getByTestId('install-plugin-input')
    await userEvent.upload(input, new File(['tarball-content'], 'slack.tar.gz', { type: 'application/gzip' }))
    // Wait for the success card to appear — the component flips to success state
    // once the MSW handler resolves.
    await canvas.findByText(/installed/i)
  },
}

// Error400 — server returns 400 with a detail message.
export const Error400: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins', () =>
          HttpResponse.json(
            { error: 'Invalid tarball', detail: 'manifest.yaml is missing required field "name".' },
            { status: 400 },
          ),
        ),
      ],
    },
  },
  args: {
    onInstalled: () => {},
    hasInstancesForPlugin: () => false,
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const input = canvas.getByTestId('install-plugin-input')
    await userEvent.upload(input, new File(['tarball-content'], 'plugin.tar.gz', { type: 'application/gzip' }))
    await canvas.findByRole('alert')
  },
}

// Error409 — concurrent update conflict; server message is surfaced verbatim.
export const Error409: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins', () =>
          HttpResponse.json(
            { error: 'concurrent plugin update; retry' },
            { status: 409 },
          ),
        ),
      ],
    },
  },
  args: {
    onInstalled: () => {},
    hasInstancesForPlugin: () => false,
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const input = canvas.getByTestId('install-plugin-input')
    await userEvent.upload(input, new File(['tarball-content'], 'plugin.tar.gz', { type: 'application/gzip' }))
    await canvas.findByRole('alert')
  },
}

// Error413 — tarball too large; fixed friendly message (no server body guaranteed).
export const Error413: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins', () =>
          new HttpResponse(null, { status: 413 }),
        ),
      ],
    },
  },
  args: {
    onInstalled: () => {},
    hasInstancesForPlugin: () => false,
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const input = canvas.getByTestId('install-plugin-input')
    await userEvent.upload(input, new File(['tarball-content'], 'plugin.tar.gz', { type: 'application/gzip' }))
    await canvas.findByRole('alert')
  },
}

// Error422 — signature validation failure; server message + detail surfaced.
export const Error422: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins', () =>
          HttpResponse.json(
            {
              error: 'Signature verification failed',
              detail: 'plugin.tar.gz.minisig: signature does not match trusted pubkey; see audit log entry audit-xyz',
            },
            { status: 422 },
          ),
        ),
      ],
    },
  },
  args: {
    onInstalled: () => {},
    hasInstancesForPlugin: () => false,
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const input = canvas.getByTestId('install-plugin-input')
    await userEvent.upload(input, new File(['tarball-content'], 'plugin.tar.gz', { type: 'application/gzip' }))
    await canvas.findByRole('alert')
  },
}

// Error503 — plugins disabled at the server level; dedicated amber notice.
export const Error503: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins', () =>
          HttpResponse.json(
            { error: 'plugin system disabled' },
            { status: 503 },
          ),
        ),
      ],
    },
  },
  args: {
    onInstalled: () => {},
    hasInstancesForPlugin: () => false,
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    const input = canvas.getByTestId('install-plugin-input')
    await userEvent.upload(input, new File(['tarball-content'], 'plugin.tar.gz', { type: 'application/gzip' }))
    await canvas.findByRole('alert')
  },
}
