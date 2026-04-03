import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import { DefaultModelSection } from './DefaultModelSection'
import { queryKeys } from '@/hooks/queryKeys'
import type { ProviderModels } from '@/hooks/queries/users'
import type { ApiPreferences } from '@/api/types'

const FIXTURE_MODELS: ProviderModels[] = [
  {
    provider: 'Anthropic',
    models: [
      { name: 'claude-opus-4-5', display_name: 'Claude Opus 4.5' },
      { name: 'claude-sonnet-4-5', display_name: 'Claude Sonnet 4.5' },
      { name: 'claude-haiku-4-5', display_name: 'Claude Haiku 4.5' },
    ],
  },
]

const FIXTURE_PREFS_WITH_DEFAULT: ApiPreferences = {
  default_model: 'claude-sonnet-4-5',
}

const FIXTURE_PREFS_NO_DEFAULT: ApiPreferences = {}

function makeQueryClient(prefs: ApiPreferences): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.models.all, FIXTURE_MODELS)
  qc.setQueryData(queryKeys.preferences.all, prefs)
  return qc
}

const meta: Meta<typeof DefaultModelSection> = {
  title: 'Settings/DefaultModelSection',
  component: DefaultModelSection,
}

export default meta
type Story = StoryObj<typeof DefaultModelSection>

export const WithDefault: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(FIXTURE_PREFS_WITH_DEFAULT)}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export const NoDefault: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(FIXTURE_PREFS_NO_DEFAULT)}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export const Loading: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}
