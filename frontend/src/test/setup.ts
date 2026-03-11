import { EventSource } from 'eventsource'
globalThis.EventSource = EventSource as unknown as typeof globalThis.EventSource

import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach, afterAll, beforeAll, vi } from 'vitest'
import { server } from './server'

beforeAll(() => server.listen())
afterEach(() => {
  cleanup()
  server.resetHandlers()
  vi.restoreAllMocks()
})
afterAll(() => server.close())
