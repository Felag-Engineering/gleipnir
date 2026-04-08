import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import ErrorBoundary from './ErrorBoundary'

const meta: Meta<typeof ErrorBoundary> = {
  title: 'ErrorBoundary/ErrorBoundary',
  component: ErrorBoundary,
}

export default meta
type Story = StoryObj<typeof ErrorBoundary>

function HealthyChild() {
  return <div>Component rendered successfully.</div>
}

function BrokenChild(): never {
  throw new Error('Simulated render crash for Storybook story')
}

export const Healthy: Story = {
  args: {
    children: <HealthyChild />,
  },
}

// The Crashed story intentionally triggers a React render error. React will log
// to console.error in development — that noise is expected and accepted. No
// suppression is attempted because any decorator's finally runs before React
// commits the error boundary, making stub/restore approaches ineffective.
export const Crashed: Story = {
  args: {
    children: <BrokenChild />,
  },
}
