import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { userEvent, within, expect } from 'storybook/test'
import '@/tokens.css'
import { AddInstanceModal } from './AddInstanceModal'

function makeQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
}

const meta: Meta<typeof AddInstanceModal> = {
  title: 'Admin/AddInstanceModal',
  component: AddInstanceModal,
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient()}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof AddInstanceModal>

function makeInstance(name: string) {
  return {
    id: `inst-${Date.now()}`,
    plugin_id: 'plugin-slack-01',
    instance_name: name,
    health_state: 'healthy',
    version: 1,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  }
}

// Default — idle, empty form ready for input.
export const Default: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins/:id/instances', () =>
          HttpResponse.json({ data: makeInstance('production') }),
        ),
      ],
    },
  },
  args: {
    pluginId: 'plugin-slack-01',
    pluginName: 'Slack',
    existingNames: [],
    onClose: () => {},
    onCreated: () => {},
  },
}

// Submitting — the endpoint never resolves, leaving the modal in the submitting
// state (spinner, disabled submit button). The play function types a name and
// clicks submit but does not wait for the response.
export const Submitting: Story = {
  parameters: {
    msw: {
      handlers: [
        // Never resolve so the component stays stuck in the submitting state.
        http.post('/api/v1/admin/plugins/:id/instances', () => new Promise(() => {})),
      ],
    },
  },
  args: {
    pluginId: 'plugin-slack-01',
    pluginName: 'Slack',
    existingNames: [],
    onClose: () => {},
    onCreated: () => {},
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.type(canvas.getByRole('textbox'), 'production')
    // Click submit — the handler never resolves, so the modal stays in
    // submitting state with the spinner visible.
    await userEvent.click(canvas.getByRole('button', { name: /create instance/i }))
  },
}

// Success — drives the modal to the success state. The MSW handler resolves
// immediately; the play function types a name and submits. On success the
// component calls onCreated + onClose — in Storybook the modal stays mounted
// because onClose is a no-op, so we can observe the spinner briefly and then
// the form resetting. We assert the submit button re-enables after the response.
export const Success: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins/:id/instances', () =>
          HttpResponse.json({ data: makeInstance('production') }),
        ),
      ],
    },
  },
  args: {
    pluginId: 'plugin-slack-01',
    pluginName: 'Slack',
    existingNames: [],
    onClose: () => {},
    onCreated: () => {},
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.type(canvas.getByRole('textbox'), 'production')
    await userEvent.click(canvas.getByRole('button', { name: /create instance/i }))
    // Wait for the mutation to complete (spinner disappears, button re-enables).
    await expect(canvas.getByRole('button', { name: /create instance/i })).not.toBeDisabled()
  },
}

// WithExistingNames — entering an already-taken name shows client-side validation.
export const WithExistingNames: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins/:id/instances', () =>
          HttpResponse.json({ data: makeInstance('production') }),
        ),
      ],
    },
  },
  args: {
    pluginId: 'plugin-slack-01',
    pluginName: 'Slack',
    existingNames: ['production', 'staging'],
    onClose: () => {},
    onCreated: () => {},
  },
}

// Error400 — server rejects the request (edge case past client validation).
export const Error400: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins/:id/instances', () =>
          HttpResponse.json(
            { error: 'instance_name must not be empty', detail: 'trimmed value was empty' },
            { status: 400 },
          ),
        ),
      ],
    },
  },
  args: {
    pluginId: 'plugin-slack-01',
    pluginName: 'Slack',
    existingNames: [],
    onClose: () => {},
    onCreated: () => {},
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.type(canvas.getByRole('textbox'), '   ')
    // Bypass client-side trim check by typing whitespace — or just type something
    // and let the server 400 show. Use a non-empty name to get past client validation.
    await userEvent.clear(canvas.getByRole('textbox'))
    await userEvent.type(canvas.getByRole('textbox'), 'something')
    await userEvent.click(canvas.getByRole('button', { name: /create instance/i }))
    await canvas.findByRole('alert')
  },
}

// Error404 — plugin disappeared between page load and submit.
export const Error404: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins/:id/instances', () =>
          HttpResponse.json({ error: 'plugin not found' }, { status: 404 }),
        ),
      ],
    },
  },
  args: {
    pluginId: 'plugin-gone',
    pluginName: 'Deleted Plugin',
    existingNames: [],
    onClose: () => {},
    onCreated: () => {},
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.type(canvas.getByRole('textbox'), 'production')
    await userEvent.click(canvas.getByRole('button', { name: /create instance/i }))
    await canvas.findByRole('alert')
  },
}

// Error409 — duplicate instance name (concurrent create from another session).
export const Error409: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins/:id/instances', () =>
          HttpResponse.json(
            { error: "instance named 'production' already exists for this plugin" },
            { status: 409 },
          ),
        ),
      ],
    },
  },
  args: {
    pluginId: 'plugin-slack-01',
    pluginName: 'Slack',
    existingNames: [],
    onClose: () => {},
    onCreated: () => {},
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.type(canvas.getByRole('textbox'), 'production')
    await userEvent.click(canvas.getByRole('button', { name: /create instance/i }))
    await canvas.findByRole('alert')
  },
}

// Error500 — unexpected server error.
export const Error500: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/admin/plugins/:id/instances', () =>
          HttpResponse.json({ error: 'internal server error' }, { status: 500 }),
        ),
      ],
    },
  },
  args: {
    pluginId: 'plugin-slack-01',
    pluginName: 'Slack',
    existingNames: [],
    onClose: () => {},
    onCreated: () => {},
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement)
    await userEvent.type(canvas.getByRole('textbox'), 'production')
    await userEvent.click(canvas.getByRole('button', { name: /create instance/i }))
    await canvas.findByRole('alert')
  },
}
