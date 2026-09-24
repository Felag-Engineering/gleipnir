import type { ApiMcpServer } from '@/api/types'

// Bounds mirror internal/mcp.MinCallTimeoutSeconds / MaxCallTimeoutSeconds
// (issue #939): 1s floor, 600s (10m) ceiling.
export const MIN_CALL_TIMEOUT_SECONDS = 1
export const MAX_CALL_TIMEOUT_SECONDS = 600

export interface ParsedCallTimeoutInput {
  value: number | null
  error: string | null
}

// parseCallTimeoutInput validates a raw text-input value for the call-timeout
// field. A blank string means "no override" (null); otherwise the value must
// be a whole number of seconds in [MIN_CALL_TIMEOUT_SECONDS,
// MAX_CALL_TIMEOUT_SECONDS]. It never returns 0 as a value — the server-side
// wire semantics treat 0 as a separate "clear" signal, not a stored value.
export function parseCallTimeoutInput(raw: string): ParsedCallTimeoutInput {
  const trimmed = raw.trim()
  if (trimmed === '') {
    return { value: null, error: null }
  }

  if (!/^-?\d+$/.test(trimmed)) {
    return { value: null, error: 'Enter a whole number of seconds' }
  }

  const n = Number(trimmed)
  if (n < MIN_CALL_TIMEOUT_SECONDS || n > MAX_CALL_TIMEOUT_SECONDS) {
    return {
      value: null,
      error: `Must be between ${MIN_CALL_TIMEOUT_SECONDS} and ${MAX_CALL_TIMEOUT_SECONDS} seconds`,
    }
  }

  return { value: n, error: null }
}

// formatCallTimeout renders a server's call timeout for display: the
// override in seconds when one is set, else "Default (Ns)" using the
// server's effective_call_timeout_seconds. When the server fixture predates
// this field (effective_call_timeout_seconds absent), it falls back to a bare
// "Default" rather than guessing a number.
export function formatCallTimeout(server: ApiMcpServer): string {
  if (server.call_timeout_seconds != null && server.call_timeout_seconds > 0) {
    return `${server.call_timeout_seconds}s`
  }
  if (server.effective_call_timeout_seconds != null) {
    return `Default (${server.effective_call_timeout_seconds}s)`
  }
  return 'Default'
}
