import type { Meta, StoryObj } from '@storybook/react-vite'
import { fn } from 'storybook/test'
import '@/tokens.css'
import ErrorFallback from './ErrorFallback'

const meta: Meta<typeof ErrorFallback> = {
  title: 'ErrorBoundary/ErrorFallback',
  component: ErrorFallback,
  args: {
    resetErrorBoundary: fn(),
  },
}

export default meta
type Story = StoryObj<typeof ErrorFallback>

export const Default: Story = {
  args: {
    error: undefined,
  },
}

export const WithError: Story = {
  args: {
    error: new Error('Failed to load policy list from the server.'),
  },
}

// The stack block only renders in DEV mode (import.meta.env.DEV). In a
// Storybook build this may not be visible, but the fixture exercises that code path.
export const WithLongStack: Story = {
  args: {
    error: (() => {
      const err = new Error('Unexpected null reference in rendering pipeline')
      err.stack = [
        'Error: Unexpected null reference in rendering pipeline',
        '    at PolicyCardExpanded (PolicyCardExpanded.tsx:42:12)',
        '    at renderWithHooks (react-dom.development.js:14985:18)',
        '    at mountIndeterminateComponent (react-dom.development.js:17811:13)',
        '    at beginWork (react-dom.development.js:19049:20)',
        '    at HTMLUnknownElement.callCallback (react-dom.development.js:3945:14)',
        '    at Object.invokeGuardedCallbackDev (react-dom.development.js:3994:16)',
        '    at invokeGuardedCallback (react-dom.development.js:4056:31)',
        '    at beginWork$1 (react-dom.development.js:23964:7)',
        '    at performUnitOfWork (react-dom.development.js:22779:12)',
        '    at workLoopSync (react-dom.development.js:22707:5)',
      ].join('\n')
      return err
    })(),
  },
}
