import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import { apiFetch, ApiError } from './fetch'

const TEST_PATH = '/test'
const TEST_URL = `/api/v1${TEST_PATH}`

describe('apiFetch', () => {
  it('unwraps the data envelope on 200', async () => {
    server.use(
      http.get(TEST_URL, () =>
        HttpResponse.json({ data: { id: '1' } }, { status: 200 })
      )
    )
    const result = await apiFetch<{ id: string }>(TEST_PATH)
    expect(result).toEqual({ id: '1' })
  })

  it('throws ApiError with status, message, and detail on JSON error body', async () => {
    server.use(
      http.get(TEST_URL, () =>
        HttpResponse.json(
          { error: 'not found', detail: 'no such policy' },
          { status: 404 }
        )
      )
    )
    let caught: unknown
    try { await apiFetch(TEST_PATH) } catch (err) { caught = err }
    expect(caught).toBeInstanceOf(ApiError)
    const apiErr = caught as ApiError
    expect(apiErr.status).toBe(404)
    expect(apiErr.message).toBe('not found')
    expect(apiErr.detail).toBe('no such policy')
  })

  it('throws ApiError falling back to statusText when error body is not JSON', async () => {
    server.use(
      http.get(TEST_URL, () =>
        HttpResponse.text('some body', { status: 500, statusText: 'Internal Server Error' })
      )
    )
    let caught: unknown
    try { await apiFetch(TEST_PATH) } catch (err) { caught = err }
    expect(caught).toBeInstanceOf(ApiError)
    const apiErr = caught as ApiError
    expect(apiErr.status).toBe(500)
    expect(apiErr.message).toBe('Internal Server Error')
  })

  it('propagates fetch rejection on network failure', async () => {
    server.use(http.get(TEST_URL, () => HttpResponse.error()))
    await expect(apiFetch(TEST_PATH)).rejects.toThrow()
  })
})
