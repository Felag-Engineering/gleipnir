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

interface BaseRequestOptions {
  skipAuthRedirect?: boolean
}

function parseApiErrorBody(body: unknown): { error: string; detail?: string } | null {
  if (
    typeof body === 'object' &&
    body !== null &&
    typeof (body as Record<string, unknown>).error === 'string'
  ) {
    const b = body as Record<string, unknown>
    return {
      error: b.error as string,
      detail: typeof b.detail === 'string' ? b.detail : undefined,
    }
  }
  return null
}

async function baseRequest(
  path: string,
  init?: RequestInit,
  opts?: BaseRequestOptions,
): Promise<Response> {
  const url = `/api/v1${path}`
  const headers: HeadersInit = {
    'Content-Type': 'application/json',
    ...init?.headers,
  }

  const response = await fetch(url, { ...init, headers })

  if (!response.ok) {
    if (response.status === 401 && !opts?.skipAuthRedirect && window.location.pathname !== '/login') {
      window.location.href = '/login?expired=1'
      throw new ApiError(401, 'Session expired')
    }
    let message = response.statusText
    let detail: string | undefined
    try {
      const body = await response.json()
      const parsed = parseApiErrorBody(body)
      if (parsed) {
        message = parsed.error
        detail = parsed.detail
      }
    } catch {
      // JSON parse failed, fall back to statusText already set above
    }
    throw new ApiError(response.status, message, detail)
  }

  return response
}

export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await baseRequest(path, init)
  const body = await response.json() as { data: T }
  return body.data
}

export async function apiFetchVoid(path: string, init?: RequestInit): Promise<void> {
  await baseRequest(path, init)
}

export { baseRequest }
