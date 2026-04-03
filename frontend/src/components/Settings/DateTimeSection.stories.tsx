import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import { DateTimeSection } from './DateTimeSection'
import { queryKeys } from '@/hooks/queryKeys'
import type { ApiPreferences } from '@/api/types'

function makeQueryClient(prefs: ApiPreferences): QueryClient {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  qc.setQueryData(queryKeys.preferences.all, prefs)
  return qc
}

const meta: Meta<typeof DateTimeSection> = {
  title: 'Settings/DateTimeSection',
  component: DateTimeSection,
}

export default meta
type Story = StoryObj<typeof DateTimeSection>

export const Absolute: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient({ timezone: 'America/New_York', date_format: 'absolute' })}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export const Relative: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient({ timezone: 'UTC', date_format: 'relative' })}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export const ISO: Story = {
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient({ timezone: 'Europe/Berlin', date_format: 'iso' })}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}
