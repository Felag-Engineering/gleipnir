import type { Meta, StoryObj } from '@storybook/react-vite'
import { createMemoryRouter, RouterProvider } from 'react-router-dom'
import '@/tokens.css'
import RouteErrorFallback from './RouteErrorFallback'

const meta: Meta = {
  title: 'ErrorBoundary/RouteErrorFallback',
}

export default meta
type Story = StoryObj

// RouteErrorFallback uses useRouteError() and useNavigate() which are only
// available inside a React Router context. We use createMemoryRouter with a
// route whose loader always throws so the errorElement renders immediately.
function RouteErrorFallbackStory() {
  const router = createMemoryRouter([
    {
      path: '/',
      loader: () => {
        throw new Error('Page failed to load: resource not found on the server.')
      },
      errorElement: <RouteErrorFallback />,
      element: <div />,
    },
  ])

  return <RouterProvider router={router} />
}

export const Default: Story = {
  render: () => <RouteErrorFallbackStory />,
}
