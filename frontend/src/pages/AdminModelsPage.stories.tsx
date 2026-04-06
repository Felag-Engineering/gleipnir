import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import AdminModelsPage from './AdminModelsPage'
import { queryKeys } from '@/hooks/queryKeys'
import type { ApiProviderStatus, ApiModelSetting } from '@/api/types'
import type { ProviderModels } from '@/hooks/queries/users'

const FIXTURE_PROVIDERS_NO_KEYS: ApiProviderStatus[] = [
  { name: 'anthropic', has_key: false },
  { name: 'openai', has_key: false },
]

const FIXTURE_PROVIDERS_WITH_KEYS: ApiProviderStatus[] = [
  { name: 'anthropic', has_key: true, masked_key: 'sk-ant-...abc1' },
  { name: 'openai', has_key: false },
]

const FIXTURE_MODELS: ProviderModels[] = [
  {
    provider: 'anthropic',
    models: [
      { name: 'claude-opus-4-5', display_name: 'Claude Opus 4.5' },
      { name: 'claude-sonnet-4-5', display_name: 'Claude Sonnet 4.5' },
    ],
  },
]

const FIXTURE_ADMIN_MODELS: ApiModelSetting[] = [
  {
    provider: 'anthropic',
    model_name: 'claude-haiku-4-5',
    enabled: false,
    updated_at: '2025-01-01T00:00:00Z',
  },
]

function makeQueryClient(opts: {
  providers: ApiProviderStatus[]
  models?: ProviderModels[]
  adminModels?: ApiModelSetting[]
  defaultModel?: string
}): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.admin.providers, opts.providers)
  if (opts.models) qc.setQueryData(queryKeys.models.all, opts.models)
  if (opts.adminModels) qc.setQueryData(queryKeys.admin.models, opts.adminModels)
  qc.setQueryData(queryKeys.admin.settings, {
    default_model: opts.defaultModel ?? '',
  })
  return qc
}

const meta: Meta<typeof AdminModelsPage> = {
  title: 'Admin/ModelsPage',
  component: AdminModelsPage,
}

export default meta
type Story = StoryObj<typeof AdminModelsPage>

export const NoKeysConfigured: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient({ providers: FIXTURE_PROVIDERS_NO_KEYS })}>
        <div style={{ maxWidth: 720 }}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
}

export const WithKeysAndModels: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider
        client={makeQueryClient({
          providers: FIXTURE_PROVIDERS_WITH_KEYS,
          models: FIXTURE_MODELS,
          adminModels: FIXTURE_ADMIN_MODELS,
          defaultModel: 'anthropic:claude-sonnet-4-5',
        })}
      >
        <div style={{ maxWidth: 720 }}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
}

export const Loading: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider
        client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
      >
        <div style={{ maxWidth: 720 }}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
}
