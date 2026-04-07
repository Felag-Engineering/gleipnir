import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { OpenAICompatProviderModal } from './OpenAICompatProviderModal'
import type { ApiOpenAICompatProvider } from '@/api/types'

const FIXTURE_PROVIDER: ApiOpenAICompatProvider = {
  id: 1,
  name: 'openai',
  base_url: 'https://api.openai.com/v1',
  masked_key: 'sk-...abcd',
  models_endpoint_available: true,
  created_at: '2026-04-01T12:00:00Z',
  updated_at: '2026-04-01T12:00:00Z',
}

function makeQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

const meta: Meta<typeof OpenAICompatProviderModal> = {
  title: 'Admin/OpenAICompatProviderModal',
  component: OpenAICompatProviderModal,
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient()}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof OpenAICompatProviderModal>

// Create mode — empty form with the OpenAI preset chip.
export const Create: Story = {
  args: {
    mode: 'create',
    onClose: () => {},
  },
}

// Edit mode — form pre-filled from an existing provider; API key field is empty and optional.
export const Edit: Story = {
  args: {
    mode: 'edit',
    provider: FIXTURE_PROVIDER,
    onClose: () => {},
  },
}

// Edit mode where the server returns a 400 error — exercises the inline error banner.
export const EditWithError: Story = {
  args: {
    mode: 'edit',
    provider: FIXTURE_PROVIDER,
    onClose: () => {},
  },
  parameters: {
    msw: {
      handlers: [
        http.put('/api/v1/admin/openai-providers/:id', () =>
          HttpResponse.json({ error: 'Name already taken' }, { status: 400 }),
        ),
      ],
    },
  },
}
