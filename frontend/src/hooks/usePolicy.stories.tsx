import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import type { ApiPolicyDetail } from '../api/types'
import { usePolicy } from './usePolicy'
import { queryKeys } from './queryKeys'

const FIXTURE_POLICY: ApiPolicyDetail = {
  id: 'pol-1',
  name: 'Nightly Backup Check',
  trigger_type: 'cron',
  folder: 'Infrastructure',
  yaml: 'name: Nightly Backup Check\ntrigger:\n  type: cron\n  schedule: "0 2 * * *"\n',
  created_at: '2026-03-01T00:00:00Z',
  updated_at: '2026-03-09T00:00:00Z',
  paused_at: null,
}

function UsePolicyDisplay({ id }: { id?: string }) {
  const { data, status, error } = usePolicy(id)
  return (
    <div style={{ fontFamily: 'IBM Plex Mono, monospace', fontSize: 13, color: '#E2E8F0', padding: 16 }}>
      <div>status: {status}</div>
      {data && (
        <pre style={{ marginTop: 8, color: '#94A3B8' }}>{JSON.stringify(data, null, 2)}</pre>
      )}
      {error && <div style={{ color: '#F87171' }}>error: {String(error)}</div>}
    </div>
  )
}

function makeQueryClient(policy?: ApiPolicyDetail): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  if (policy) {
    qc.setQueryData(queryKeys.policies.detail(policy.id), policy)
  }
  return qc
}

const meta: Meta<typeof UsePolicyDisplay> = {
  title: 'Hooks/usePolicy',
  component: UsePolicyDisplay,
}

export default meta
type Story = StoryObj<typeof UsePolicyDisplay>

export const Loaded: Story = {
  args: { id: 'pol-1' },
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient(FIXTURE_POLICY)}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export const NoId: Story = {
  args: { id: undefined },
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient()}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}
