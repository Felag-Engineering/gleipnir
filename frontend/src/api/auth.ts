// Auth API calls use raw fetch (not apiFetch) to avoid the 401 redirect loop
// in apiFetch — the login page itself must be able to receive a 401 response.

export async function login(username: string, password: string): Promise<{ username: string }> {
  const response = await fetch('/api/v1/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })

  if (!response.ok) {
    let message = response.statusText
    try {
      const body = await response.json() as { error: string }
      message = body.error
    } catch {
      // JSON parse failed, fall back to statusText already set above
    }
    throw new Error(message)
  }

  const body = await response.json() as { data: { username: string } }
  return body.data
}

export async function logout(): Promise<void> {
  await fetch('/api/v1/auth/logout', { method: 'POST' })
}
