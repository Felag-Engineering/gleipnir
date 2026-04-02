import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import { RunActivityChart } from './RunActivityChart'
import type { ApiTimeSeriesResponse } from '@/api/types'
import storyStyles from '../dashboard-stories.module.css'

const meta: Meta<typeof RunActivityChart> = {
  title: 'Dashboard/RunActivityChart',
  component: RunActivityChart,
  decorators: [
    (Story) => (
      <QueryClientProvider client={new QueryClient()}>
        <div className={storyStyles.storyWrapper}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof RunActivityChart>

function makeBuckets(overrides?: Partial<ApiTimeSeriesResponse['buckets'][0]>[]): ApiTimeSeriesResponse {
  const now = new Date()
  const buckets = Array.from({ length: 24 }, (_, i) => {
    const ts = new Date(now.getTime() - (23 - i) * 3600 * 1000)
    return {
      timestamp: ts.toISOString(),
      completed: 0,
      failed: 0,
      waiting_for_approval: 0,
      waiting_for_feedback: 0,
      cost_by_model: {},
      ...overrides?.[i],
    }
  })
  return { buckets }
}

const MOCK_DATA: ApiTimeSeriesResponse = makeBuckets([
  {}, {}, {},
  { completed: 3 },
  { completed: 5, failed: 1 },
  { completed: 8, waiting_for_approval: 2 },
  { completed: 4 },
  { completed: 6, failed: 1 },
  { completed: 12 },
  { completed: 9, waiting_for_approval: 1 },
  { completed: 15, failed: 2 },
  { completed: 7 },
  { completed: 11 },
  { completed: 5, waiting_for_approval: 1 },
  { completed: 3, failed: 1 },
  { completed: 8 },
  { completed: 6 },
  { completed: 14, waiting_for_approval: 2 },
  { completed: 9, failed: 1 },
  { completed: 7 },
  { completed: 5, waiting_for_approval: 1 },
  { completed: 4 },
  { completed: 2 },
  { completed: 1 },
])

export const Default: Story = {
  args: { data: MOCK_DATA, isLoading: false },
}

export const Empty: Story = {
  args: { data: makeBuckets(), isLoading: false },
}

export const Loading: Story = {
  args: { data: undefined, isLoading: true },
}
