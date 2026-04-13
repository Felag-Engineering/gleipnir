import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import { CostByModelChart } from './CostByModelChart'
import type { ApiTimeSeriesResponse } from '@/api/types'
import storyStyles from '../dashboard-stories.module.css'

const meta: Meta<typeof CostByModelChart> = {
  title: 'Dashboard/CostByModelChart',
  component: CostByModelChart,
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
type Story = StoryObj<typeof CostByModelChart>

function makeBuckets(
  overrides?: Partial<ApiTimeSeriesResponse['buckets'][0]>[],
): ApiTimeSeriesResponse {
  const now = new Date()
  const buckets = Array.from({ length: 24 }, (_, i) => {
    const ts = new Date(now.getTime() - (23 - i) * 3600 * 1000)
    return {
      timestamp: ts.toISOString(),
      completed: 0,
      failed: 0,
      waiting_for_approval: 0,
      waiting_for_feedback: 0,
      cost_by_model: {} as Record<string, number>,
      ...overrides?.[i],
    }
  })
  return { buckets }
}

const MULTI_MODEL: ApiTimeSeriesResponse = makeBuckets([
  {}, {}, {},
  { cost_by_model: { 'Sonnet 4': 5000, 'Haiku 3.5': 2000 } },
  { cost_by_model: { 'Sonnet 4': 8000, 'Haiku 3.5': 3000, 'Opus 4': 1000 } },
  { cost_by_model: { 'Sonnet 4': 12000 } },
  { cost_by_model: { 'Sonnet 4': 6000, 'Haiku 3.5': 4000 } },
  { cost_by_model: { 'Opus 4': 3000 } },
  { cost_by_model: { 'Sonnet 4': 9000, 'Haiku 3.5': 1500 } },
  { cost_by_model: { 'Sonnet 4': 7000 } },
  { cost_by_model: { 'Haiku 3.5': 5000, 'Opus 4': 2000 } },
  { cost_by_model: { 'Sonnet 4': 11000 } },
  {},
  { cost_by_model: { 'Sonnet 4': 4000, 'Haiku 3.5': 2500 } },
  {},
  { cost_by_model: { 'Sonnet 4': 8000 } },
  {},
  { cost_by_model: { 'Sonnet 4': 6000, 'Haiku 3.5': 3000 } },
  { cost_by_model: { 'Opus 4': 4000 } },
  { cost_by_model: { 'Sonnet 4': 5000 } },
  {},
  { cost_by_model: { 'Sonnet 4': 3000, 'Haiku 3.5': 1000 } },
  {},
  { cost_by_model: { 'Sonnet 4': 2000 } },
])

const SINGLE_MODEL: ApiTimeSeriesResponse = makeBuckets([
  {}, {}, {},
  { cost_by_model: { 'Haiku 3.5': 10000 } },
  { cost_by_model: { 'Haiku 3.5': 15000 } },
  { cost_by_model: { 'Haiku 3.5': 8000 } },
])

export const Default: Story = {
  args: { data: MULTI_MODEL, isLoading: false },
}

export const SingleModel: Story = {
  args: { data: SINGLE_MODEL, isLoading: false },
}

export const Empty: Story = {
  args: { data: makeBuckets(), isLoading: false },
}

export const Loading: Story = {
  args: { data: undefined, isLoading: true },
}

// SubPennyCosts exercises the sub-penny formatting path (toFixed(4) for
// costs < $0.01). Uses cheap models with small token counts to produce
// values like $0.0001 that would previously round to $0.00.
const SUB_PENNY: ApiTimeSeriesResponse = makeBuckets([
  {}, {}, {},
  { cost_by_model: { 'Haiku 4.5': 500, 'GPT-5 Nano': 800 } },
  { cost_by_model: { 'Haiku 4.5': 750, 'Gemini 2.5 Flash-Lite': 600 } },
  { cost_by_model: { 'GPT-5 Nano': 1200 } },
  { cost_by_model: { 'Haiku 4.5': 2200, 'Gemini 2.5 Flash-Lite': 576 } },
  { cost_by_model: { 'GPT-5 Nano': 1400 } },
])

export const SubPennyCosts: Story = {
  args: { data: SUB_PENNY, isLoading: false },
}
