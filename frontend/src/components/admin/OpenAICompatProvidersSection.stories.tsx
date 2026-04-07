import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import { OpenAICompatProvidersSection } from './OpenAICompatProvidersSection'
import { queryKeys } from '@/hooks/queryKeys'
import type { ApiOpenAICompatProvider } from '@/api/types'

const FIXTURE_PROVIDER_OK: ApiOpenAICompatProvider = {
  id: 1,
  name: 'openai',
  base_url: 'https://api.openai.com/v1',
  masked_key: 'sk-...abcd',
  models_endpoint_available: true,
  created_at: '2026-04-01T12:00:00Z',
  updated_at: '2026-04-01T12:00:00Z',
}

const FIXTURE_PROVIDER_NO_MODELS: ApiOpenAICompatProvider = {
  id: 2,
  name: 'ollama-local',
  base_url: 'http://localhost:11434/v1',
  masked_key: '...',
  models_endpoint_available: false,
  created_at: '2026-04-02T09:00:00Z',
  updated_at: '2026-04-02T09:00:00Z',
}

function makeQueryClient(rows: ApiOpenAICompatProvider[]): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.admin.openaiCompatProviders, rows)
  return qc
}

const meta: Meta<typeof OpenAICompatProvidersSection> = {
  title: 'Admin/OpenAICompatProvidersSection',
  component: OpenAICompatProvidersSection,
}

export default meta
type Story = StoryObj<typeof OpenAICompatProvidersSection>

// Loaded table — one provider with a working models endpoint.
export const Default: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient([FIXTURE_PROVIDER_OK])}>
        <div style={{ maxWidth: 800 }}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
}

// Empty state — no providers configured; shows the empty-state block and "Add provider" button.
export const Empty: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient([])}>
        <div style={{ maxWidth: 800 }}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
}

// A provider whose models endpoint is unavailable — exercises the amber warning badge.
export const ModelsEndpointUnavailable: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient([FIXTURE_PROVIDER_NO_MODELS])}>
        <div style={{ maxWidth: 800 }}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
}
