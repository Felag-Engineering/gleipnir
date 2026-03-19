export class ApiError extends Error {
  status: number
  detail?: string

  constructor(status: number, message: string, detail?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.detail = detail
  }
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const url = `/api/v1${path}`

  const headers: HeadersInit = {
    ...(init?.body != null ? { 'Content-Type': 'application/json' } : {}),
    ...init?.headers,
  }

  const response = await fetch(url, { ...init, headers })

  if (!response.ok) {
    if (response.status === 401 && window.location.pathname !== '/login') {
      window.location.href = '/login'
      return Promise.reject()
    }
    let message = response.statusText
    let detail: string | undefined
    try {
      const body = await response.json() as { error: string; detail?: string }
      message = body.error
      detail = body.detail
    } catch {
      // JSON parse failed, fall back to statusText already set above
    }
    throw new ApiError(response.status, message, detail)
  }

  const body = await response.json() as { data: T }
  return body.data
}

export async function apiFetchVoid(path: string, init?: RequestInit): Promise<void> {
  const url = `/api/v1${path}`

  const headers: HeadersInit = {
    ...(init?.body != null ? { 'Content-Type': 'application/json' } : {}),
    ...init?.headers,
  }

  const response = await fetch(url, { ...init, headers })

  if (!response.ok) {
    if (response.status === 401 && window.location.pathname !== '/login') {
      window.location.href = '/login'
      return
    }
    let message = response.statusText
    let detail: string | undefined
    try {
      const body = await response.json() as { error: string; detail?: string }
      message = body.error
      detail = body.detail
    } catch {
      // JSON parse failed, fall back to statusText already set above
    }
    throw new ApiError(response.status, message, detail)
  }
}
