// Auth API calls use skipAuthRedirect to avoid the 401 redirect loop —
// the login page itself must be able to receive a 401 response.
import { ApiError, baseRequest } from './fetch'

export async function getAuthStatus(): Promise<{ setup_required: boolean }> {
  const response = await baseRequest('/auth/status', undefined, { skipAuthRedirect: true })
  const body = await response.json() as { data: { setup_required: boolean } }
  return body.data
}

export async function setup(username: string, password: string): Promise<{ username: string }> {
  const response = await baseRequest(
    '/auth/setup',
    { method: 'POST', body: JSON.stringify({ username, password }) },
    { skipAuthRedirect: true },
  )
  const body = await response.json() as { data: { username: string } }
  return body.data
}

export async function login(username: string, password: string): Promise<{ username: string }> {
  const response = await baseRequest(
    '/auth/login',
    { method: 'POST', body: JSON.stringify({ username, password }) },
    { skipAuthRedirect: true },
  )
  const body = await response.json() as { data: { username: string } }
  return body.data
}

export async function logout(): Promise<void> {
  await fetch('/api/v1/auth/logout', { method: 'POST' })
}

export { ApiError }
