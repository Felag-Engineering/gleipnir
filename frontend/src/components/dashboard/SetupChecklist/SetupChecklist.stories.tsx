import type { Meta, StoryObj } from '@storybook/react-vite'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/tokens.css'
import { SetupChecklist } from './SetupChecklist'
import storyStyles from '../dashboard-stories.module.css'

const meta: Meta<typeof SetupChecklist> = {
  title: 'Dashboard/SetupChecklist',
  component: SetupChecklist,
  decorators: [
    (Story) => (
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <div className={storyStyles.storyWrapper}>
            <Story />
          </div>
        </MemoryRouter>
      </QueryClientProvider>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof SetupChecklist>

export const AllIncomplete: Story = {
  args: {
    hasModel: false,
    hasServer: false,
    hasAgent: false,
    hasFirstRun: false,
    isLoading: false,
  },
}

export const ModelDone: Story = {
  args: {
    hasModel: true,
    hasServer: false,
    hasAgent: false,
    hasFirstRun: false,
    isLoading: false,
  },
}

export const ServerDone: Story = {
  args: {
    hasModel: true,
    hasServer: true,
    hasAgent: false,
    hasFirstRun: false,
    isLoading: false,
  },
}

export const AgentDone: Story = {
  args: {
    hasModel: true,
    hasServer: true,
    hasAgent: true,
    hasFirstRun: false,
    isLoading: false,
  },
}

export const Loading: Story = {
  args: {
    hasModel: false,
    hasServer: false,
    hasAgent: false,
    hasFirstRun: false,
    isLoading: true,
  },
}
