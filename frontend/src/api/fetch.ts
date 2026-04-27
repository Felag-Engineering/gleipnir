export interface ApiErrorIssue {
  field?: string
  message: string
}

export class ApiError extends Error {
  status: number
  detail?: string
  issues?: ApiErrorIssue[]
  // runId is populated when the backend failed to launch a run *after* the
  // run row was created — the row has been transitioned to failed with the
  // underlying error stored on it. Callers can deep-link to /runs/:runId to
  // show the operator the recorded reason.
  runId?: string

  constructor(
    status: number,
    message: string,
    detail?: string,
    issues?: ApiErrorIssue[],
    runId?: string,
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.detail = detail
    this.issues = issues
    this.runId = runId
  }
}

interface BaseRequestOptions {
  skipAuthRedirect?: boolean
}

function parseApiErrorBody(
  body: unknown,
): { error: string; detail?: string; issues?: ApiErrorIssue[]; runId?: string } | null {
  if (
    typeof body === 'object' &&
    body !== null &&
    typeof (body as Record<string, unknown>).error === 'string'
  ) {
    const b = body as Record<string, unknown>
    let issues: ApiErrorIssue[] | undefined
    if (Array.isArray(b.issues)) {
      const parsed = b.issues.filter(
        (item): item is ApiErrorIssue =>
          typeof item === 'object' &&
          item !== null &&
          typeof (item as Record<string, unknown>).message === 'string',
      )
      if (parsed.length > 0) issues = parsed
    }
    return {
      error: b.error as string,
      detail: typeof b.detail === 'string' ? b.detail : undefined,
      issues,
      runId: typeof b.run_id === 'string' && b.run_id !== '' ? b.run_id : undefined,
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
    let issues: ApiErrorIssue[] | undefined
    let runId: string | undefined
    try {
      const body = await response.json()
      const parsed = parseApiErrorBody(body)
      if (parsed) {
        message = parsed.error
        detail = parsed.detail
        issues = parsed.issues
        runId = parsed.runId
      }
    } catch {
      // JSON parse failed, fall back to statusText already set above
    }
    throw new ApiError(response.status, message, detail, issues, runId)
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
