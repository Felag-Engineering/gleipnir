import type { Meta, StoryObj } from '@storybook/react-vite'
import QueryBoundary from './QueryBoundary'
import SkeletonList from './SkeletonList'
import '@/tokens.css'

const meta: Meta<typeof QueryBoundary> = {
  title: 'Shared/QueryBoundary',
  component: QueryBoundary,
}

export default meta
type Story = StoryObj<typeof QueryBoundary>

export const Loading: Story = {
  args: {
    status: 'pending',
    children: <span>Content</span>,
  },
}

export const Error: Story = {
  args: {
    status: 'error',
    errorMessage: 'Something went wrong.',
    children: <span>Content</span>,
  },
}

export const ErrorWithRetry: Story = {
  args: {
    status: 'error',
    errorMessage: 'Failed to load items.',
    onRetry: () => {},
    children: <span>Content</span>,
  },
}

export const Empty: Story = {
  args: {
    status: 'success',
    isEmpty: true,
    children: <span>Content</span>,
  },
}

export const EmptyWithCustomState: Story = {
  args: {
    status: 'success',
    isEmpty: true,
    emptyState: <p style={{ textAlign: 'center', color: 'var(--text-second)' }}>No items found.</p>,
    children: <span>Content</span>,
  },
}

export const Success: Story = {
  args: {
    status: 'success',
    children: <span>Content loaded successfully.</span>,
  },
}

export const LoadingCustomSkeleton: Story = {
  name: 'Loading (custom skeleton)',
  args: {
    status: 'pending',
    skeleton: <SkeletonList count={3} height={120} gap={12} borderRadius={8} />,
    children: <span>Content</span>,
  },
}
