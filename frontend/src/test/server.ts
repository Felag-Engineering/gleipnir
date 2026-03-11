import { setupServer } from 'msw/node'

// Start with no handlers. Each test file adds handlers via server.use().
export const server = setupServer()
