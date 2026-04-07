import { http, HttpResponse } from 'msw'
import type {
  ApiOpenAICompatProvider,
  ApiOpenAICompatProviderUpsert,
} from '@/api/types'

// Module-level seed row. Mutations (POST/PUT/DELETE) update this array so
// stories can demonstrate realistic create/edit/delete flows without hitting
// the network. Use server.use(...) overrides in tests that need isolation.
const rows: ApiOpenAICompatProvider[] = [
  {
    id: 1,
    name: 'openai',
    base_url: 'https://api.openai.com/v1',
    masked_key: 'sk-...abcd',
    models_endpoint_available: true,
    created_at: '2026-04-01T12:00:00Z',
    updated_at: '2026-04-01T12:00:00Z',
  },
]

// Mimic the backend MaskKey format: prefix up to the first dash + "..." + last 4 chars.
// Example: "sk-abcdef1234" → "sk-...1234"
function maskKey(apiKey: string): string {
  const dashIdx = apiKey.indexOf('-')
  const prefix = dashIdx >= 0 ? apiKey.slice(0, dashIdx + 1) : apiKey.slice(0, 3)
  return `${prefix}...${apiKey.slice(-4)}`
}

// Sentinel used by the backend when an update request leaves the key unchanged.
const MASKED_SENTINEL = '...'

export const openaiCompatProvidersHandlers = [
  http.get('/api/v1/admin/openai-providers', () =>
    HttpResponse.json({ data: rows }),
  ),

  http.get('/api/v1/admin/openai-providers/:id', ({ params }) => {
    const id = Number(params.id)
    const row = rows.find((r) => r.id === id)
    if (!row) return new HttpResponse(null, { status: 404 })
    return HttpResponse.json({ data: row })
  }),

  http.post('/api/v1/admin/openai-providers', async ({ request }) => {
    const body = (await request.json()) as ApiOpenAICompatProviderUpsert
    const row: ApiOpenAICompatProvider = {
      id: rows.length + 1,
      name: body.name,
      base_url: body.base_url,
      masked_key: maskKey(body.api_key),
      models_endpoint_available: true,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }
    rows.push(row)
    return HttpResponse.json({ data: row }, { status: 201 })
  }),

  http.put('/api/v1/admin/openai-providers/:id', async ({ params, request }) => {
    const id = Number(params.id)
    const body = (await request.json()) as ApiOpenAICompatProviderUpsert
    const idx = rows.findIndex((r) => r.id === id)
    if (idx < 0) return new HttpResponse(null, { status: 404 })
    // If api_key contains the masked sentinel the client sends when it wants to
    // leave the key unchanged, keep the existing masked_key rather than
    // re-masking the placeholder.
    const newMaskedKey = body.api_key.includes(MASKED_SENTINEL)
      ? rows[idx].masked_key
      : maskKey(body.api_key)
    rows[idx] = {
      ...rows[idx],
      name: body.name,
      base_url: body.base_url,
      masked_key: newMaskedKey,
      updated_at: new Date().toISOString(),
    }
    return HttpResponse.json({ data: rows[idx] })
  }),

  http.delete('/api/v1/admin/openai-providers/:id', ({ params }) => {
    const id = Number(params.id)
    const idx = rows.findIndex((r) => r.id === id)
    if (idx < 0) return new HttpResponse(null, { status: 404 })
    rows.splice(idx, 1)
    return new HttpResponse(null, { status: 204 })
  }),

  http.post('/api/v1/admin/openai-providers/:id/test', () =>
    HttpResponse.json({ data: { ok: true, models_endpoint_available: true } }),
  ),
]
