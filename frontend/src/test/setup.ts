import { EventSource } from 'eventsource'
globalThis.EventSource = EventSource as unknown as typeof globalThis.EventSource

import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach, afterAll, beforeAll, vi } from 'vitest'
import { server } from './server'

// jsdom does not implement matchMedia. Provide a minimal, no-match default so
// components that read it (e.g. Layout's mobile-breakpoint listener) render
// without throwing. Individual tests may override via Object.defineProperty.
if (!window.matchMedia) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    }),
  })
}

beforeAll(() => server.listen())
afterEach(() => {
  cleanup()
  server.resetHandlers()
  vi.restoreAllMocks()
})
afterAll(() => server.close())
