import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { ChangePasswordSection } from './ChangePasswordSection'

const meta: Meta<typeof ChangePasswordSection> = {
  title: 'Settings/ChangePasswordSection',
  component: ChangePasswordSection,
  decorators: [
    (Story) => (
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <Story />
      </QueryClientProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof ChangePasswordSection>

export const Default: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/auth/password', () => HttpResponse.json({ data: {} })),
      ],
    },
  },
}

export const ServerError: Story = {
  parameters: {
    msw: {
      handlers: [
        http.post('/api/v1/auth/password', () =>
          HttpResponse.json({ error: 'Current password is incorrect.' }, { status: 401 }),
        ),
      ],
    },
  },
}
