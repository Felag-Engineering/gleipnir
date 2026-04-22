import type { Meta, StoryObj } from '@storybook/react'
import { ErrorBanner } from './ErrorBanner'

const meta: Meta<typeof ErrorBanner> = {
  title: 'Form/ErrorBanner',
  component: ErrorBanner,
}

export default meta

type Story = StoryObj<typeof ErrorBanner>

export const NoIssues: Story = {
  args: {
    issues: [],
  },
}

export const OneIssue: Story = {
  args: {
    issues: [{ field: 'name', message: 'Name is required' }],
    onIssueClick: (field) => console.log('clicked', field),
  },
}

export const ManyIssues: Story = {
  args: {
    issues: [
      { field: 'name', message: 'Name is required' },
      { field: 'agent.task', message: 'Task is required' },
      { field: 'model.provider', message: 'Provider is required' },
      { field: 'capabilities.tools[0].tool', message: 'Tool must use dot notation' },
      { field: 'agent.concurrency', message: '"replace" is not valid when any tool has approval: required' },
    ],
    onIssueClick: (field) => console.log('clicked', field),
    onDismiss: () => console.log('dismissed'),
  },
}

export const ServerOnlyIssues: Story = {
  args: {
    issues: [
      { message: 'Server returned an unexpected error' },
      { message: 'Rate limit exceeded; please wait before retrying' },
    ],
  },
}

export const WithDismiss: Story = {
  args: {
    issues: [{ field: 'model.provider', message: 'Provider is required' }],
    onDismiss: () => console.log('dismissed'),
    onIssueClick: (field) => console.log('clicked', field),
  },
}
