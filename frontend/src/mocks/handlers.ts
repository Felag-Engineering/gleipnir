import { http, HttpResponse } from 'msw'
import { openaiCompatProvidersHandlers } from '@/mocks/openaiCompatProviders'

// Default handlers registered globally in Storybook so that all stories
// involving the Layout component get safe responses without triggering network
// errors.
//
// Most admin endpoint handlers (/api/v1/admin/...) are excluded because admin
// page stories pre-seed their QueryClients via setQueryData and never fire
// those queries. The openai-compat handlers are an exception: they are
// included here to support future stories and tests that render the new
// OpenAI-compatible provider components without pre-seeding the QueryClient.
//
// /api/v1/events (SSE) is excluded because MSW v2 does not intercept
// EventSource connections. The useSSE hook will show reconnecting state in
// Storybook, which is acceptable.

export const defaultHandlers = [
  http.get('/api/v1/auth/me', () =>
    HttpResponse.json({ data: { id: '1', username: 'operator', roles: ['operator'] } }),
  ),

  http.get('/api/v1/attention', () => HttpResponse.json({ data: { items: [] } })),

  http.get('/api/v1/mcp/servers', () => HttpResponse.json({ data: [] })),

  ...openaiCompatProvidersHandlers,
]
