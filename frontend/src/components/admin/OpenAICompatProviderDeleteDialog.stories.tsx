import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { OpenAICompatProviderDeleteDialog } from './OpenAICompatProviderDeleteDialog'
import { ApiError } from '@/api/fetch'

const FIXTURE_PROVIDER = {
  id: 1,
  name: 'openai',
  base_url: 'https://api.openai.com/v1',
  masked_key: 'sk-...abcd',
  models_endpoint_available: true,
  created_at: '2026-04-01T12:00:00Z',
  updated_at: '2026-04-01T12:00:00Z',
}

const meta: Meta<typeof OpenAICompatProviderDeleteDialog> = {
  title: 'Admin/OpenAICompatProviderDeleteDialog',
  component: OpenAICompatProviderDeleteDialog,
}

export default meta
type Story = StoryObj<typeof OpenAICompatProviderDeleteDialog>

export const Default: Story = {
  args: {
    provider: FIXTURE_PROVIDER,
    isPending: false,
    onClose: () => {},
    onConfirm: () => {},
  },
}

export const Pending: Story = {
  args: {
    provider: FIXTURE_PROVIDER,
    isPending: true,
    onClose: () => {},
    onConfirm: () => {},
  },
}

export const WithError: Story = {
  args: {
    provider: FIXTURE_PROVIDER,
    isPending: false,
    error: new ApiError(500, 'Failed to delete provider', 'Internal server error'),
    onClose: () => {},
    onConfirm: () => {},
  },
}
