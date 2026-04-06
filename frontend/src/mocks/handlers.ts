import { http, HttpResponse } from 'msw'

// Default handlers for the three endpoints that the Layout component's hooks
// call on every render. These are registered globally in Storybook so that
// all stories involving Layout get safe empty responses without triggering
// network errors.
//
// Admin endpoint handlers (/api/v1/admin/...) are intentionally excluded:
// admin page stories pre-seed their QueryClients via setQueryData, so those
// queries never fire and global handlers would be redundant maintenance.
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
]
