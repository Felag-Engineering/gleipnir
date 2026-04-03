import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import { apiFetch, apiFetchVoid, ApiError } from './fetch'
import { login, setup, getAuthStatus } from './auth'

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

  it('throws ApiError on 401 (not silently returns)', async () => {
    server.use(
      http.get(TEST_URL, () =>
        HttpResponse.json({ error: 'Session expired' }, { status: 401 })
      )
    )
    let caught: unknown
    try { await apiFetch(TEST_PATH) } catch (err) { caught = err }
    expect(caught).toBeInstanceOf(ApiError)
    const apiErr = caught as ApiError
    expect(apiErr.status).toBe(401)
  })

  it('propagates fetch rejection on network failure', async () => {
    server.use(http.get(TEST_URL, () => HttpResponse.error()))
    await expect(apiFetch(TEST_PATH)).rejects.toThrow()
  })

  it('always sets Content-Type: application/json', async () => {
    let capturedContentType: string | null = null
    server.use(
      http.post(TEST_URL, ({ request }) => {
        capturedContentType = request.headers.get('Content-Type')
        return HttpResponse.json({ data: { ok: true } }, { status: 200 })
      })
    )
    const result = await apiFetch<{ ok: boolean }>(TEST_PATH, { method: 'POST' })
    expect(result).toEqual({ ok: true })
    expect(capturedContentType).toBe('application/json')
  })

  it('sets window.location.href on 401 when not on login page', async () => {
    const originalLocation = window.location
    let capturedHref = 'http://localhost:3000/dashboard'
    const mockLocation = { pathname: '/dashboard' }
    Object.defineProperty(mockLocation, 'href', {
      get: () => capturedHref,
      set: (v: string) => { capturedHref = v },
      configurable: true,
    })
    Object.defineProperty(window, 'location', {
      value: mockLocation,
      writable: true,
      configurable: true,
    })

    server.use(
      http.get(TEST_URL, () =>
        HttpResponse.json({ error: 'unauthorized' }, { status: 401 })
      )
    )

    try {
      let caught: unknown
      try { await apiFetch(TEST_PATH) } catch (err) { caught = err }
      // The code now throws ApiError(401, 'Session expired') after setting href
      expect(caught).toBeInstanceOf(ApiError)
      expect((caught as ApiError).status).toBe(401)
      expect(capturedHref).toBe('/login?expired=1')
    } finally {
      Object.defineProperty(window, 'location', {
        value: originalLocation,
        writable: true,
        configurable: true,
      })
    }
  })

  it('throws ApiError on 401 when already on /login (no redirect)', async () => {
    const originalLocation = window.location
    Object.defineProperty(window, 'location', {
      value: { pathname: '/login', href: 'http://localhost:3000/login' },
      writable: true,
      configurable: true,
    })

    try {
      server.use(
        http.get(TEST_URL, () =>
          HttpResponse.json({ error: 'unauthorized' }, { status: 401 })
        )
      )

      let caught: unknown
      try { await apiFetch(TEST_PATH) } catch (err) { caught = err }
      expect(caught).toBeInstanceOf(ApiError)
      expect((caught as ApiError).status).toBe(401)
      expect((caught as ApiError).message).toBe('unauthorized')
    } finally {
      Object.defineProperty(window, 'location', {
        value: originalLocation,
        writable: true,
        configurable: true,
      })
    }
  })
})

describe('apiFetchVoid', () => {
  it('resolves without a value on 200', async () => {
    server.use(
      http.post(TEST_URL, () => HttpResponse.json({}, { status: 200 }))
    )
    await expect(apiFetchVoid(TEST_PATH, { method: 'POST' })).resolves.toBeUndefined()
  })

  it('throws ApiError on non-OK response', async () => {
    server.use(
      http.post(TEST_URL, () =>
        HttpResponse.json({ error: 'bad request' }, { status: 400 })
      )
    )
    let caught: unknown
    try { await apiFetchVoid(TEST_PATH, { method: 'POST' }) } catch (err) { caught = err }
    expect(caught).toBeInstanceOf(ApiError)
    const apiErr = caught as ApiError
    expect(apiErr.status).toBe(400)
    expect(apiErr.message).toBe('bad request')
  })

  it('throws ApiError on 401 (not silently returns void)', async () => {
    server.use(
      http.post(TEST_URL, () =>
        HttpResponse.json({ error: 'Session expired' }, { status: 401 })
      )
    )
    let caught: unknown
    try { await apiFetchVoid(TEST_PATH, { method: 'POST' }) } catch (err) { caught = err }
    expect(caught).toBeInstanceOf(ApiError)
    expect((caught as ApiError).status).toBe(401)
  })
})

describe('auth functions', () => {
  it('getAuthStatus returns setup_required on 200', async () => {
    server.use(
      http.get('/api/v1/auth/status', () =>
        HttpResponse.json({ data: { setup_required: false } }, { status: 200 })
      )
    )
    const result = await getAuthStatus()
    expect(result).toEqual({ setup_required: false })
  })

  it('getAuthStatus throws ApiError on failure', async () => {
    server.use(
      http.get('/api/v1/auth/status', () =>
        HttpResponse.json({ error: 'server error' }, { status: 500 })
      )
    )
    let caught: unknown
    try { await getAuthStatus() } catch (err) { caught = err }
    expect(caught).toBeInstanceOf(ApiError)
    expect((caught as ApiError).status).toBe(500)
  })

  it('login returns user data on 200', async () => {
    server.use(
      http.post('/api/v1/auth/login', () =>
        HttpResponse.json({ data: { username: 'admin' } }, { status: 200 })
      )
    )
    const result = await login('admin', 'secret')
    expect(result).toEqual({ username: 'admin' })
  })

  it('login throws ApiError (not plain Error) on 401', async () => {
    server.use(
      http.post('/api/v1/auth/login', () =>
        HttpResponse.json({ error: 'invalid credentials' }, { status: 401 })
      )
    )
    let caught: unknown
    try { await login('admin', 'wrong') } catch (err) { caught = err }
    expect(caught).toBeInstanceOf(ApiError)
    const apiErr = caught as ApiError
    expect(apiErr.status).toBe(401)
    expect(apiErr.message).toBe('invalid credentials')
  })

  it('setup throws ApiError on failure', async () => {
    server.use(
      http.post('/api/v1/auth/setup', () =>
        HttpResponse.json({ error: 'username taken', detail: 'choose another' }, { status: 409 })
      )
    )
    let caught: unknown
    try { await setup('admin', 'pass') } catch (err) { caught = err }
    expect(caught).toBeInstanceOf(ApiError)
    const apiErr = caught as ApiError
    expect(apiErr.status).toBe(409)
    expect(apiErr.message).toBe('username taken')
    expect(apiErr.detail).toBe('choose another')
  })
})
