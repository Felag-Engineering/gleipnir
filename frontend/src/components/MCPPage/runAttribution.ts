import type { ApiMcpServer, RunAttributionMode, RunAttributionRequest } from '@/api/types'

// RELAY_PRESET_HEADERS mirrors the Go constants in internal/mcp/attribution.go
// (RelayOnBehalfOfHeader / RelaySessionRefHeader / RelayTraceparentHeader).
// Display only — the server never accepts custom names for mode 'relay'.
export const RELAY_PRESET_HEADERS = {
  onBehalfOf: 'X-Relay-On-Behalf-Of',
  sessionRef: 'X-Relay-Session-Ref',
  traceparent: 'traceparent',
} as const

export const MODE_LABELS: Record<RunAttributionMode, string> = {
  off: 'Off',
  relay: 'Relay preset',
  custom: 'Custom headers',
}

// formatRunAttribution renders a server's effective run attribution setting
// for display, e.g. "Off", or
// "Relay preset: X-Relay-On-Behalf-Of, X-Relay-Session-Ref, traceparent".
// Falls back to "Off" for an old fixture with no run_attribution field at
// all, matching the server's own "absent = off" reading.
export function formatRunAttribution(server: ApiMcpServer): string {
  const attribution = server.run_attribution
  if (!attribution || attribution.mode === 'off') {
    return MODE_LABELS.off
  }
  const names = [
    attribution.on_behalf_of_header,
    attribution.session_ref_header,
    attribution.traceparent_header,
  ].filter((name) => name !== '')
  return `${MODE_LABELS[attribution.mode]}: ${names.join(', ')}`
}

export interface CustomHeaderNames {
  onBehalfOfHeader: string
  sessionRefHeader: string
  traceparentHeader: string
}

// buildRunAttributionRequest builds the wire request for the given mode: off
// clears with {mode:'off'}, relay sends just {mode:'relay'} (the preset
// names are server-side constants, never sent over the wire), and custom
// trims and includes only the non-empty names.
export function buildRunAttributionRequest(
  mode: RunAttributionMode,
  names: CustomHeaderNames,
): RunAttributionRequest {
  if (mode === 'off') return { mode: 'off' }
  if (mode === 'relay') return { mode: 'relay' }

  const request: RunAttributionRequest = { mode: 'custom' }
  const onBehalfOfHeader = names.onBehalfOfHeader.trim()
  const sessionRefHeader = names.sessionRefHeader.trim()
  const traceparentHeader = names.traceparentHeader.trim()
  if (onBehalfOfHeader) request.on_behalf_of_header = onBehalfOfHeader
  if (sessionRefHeader) request.session_ref_header = sessionRefHeader
  if (traceparentHeader) request.traceparent_header = traceparentHeader
  return request
}

// HEADER_NAME_PATTERN is a light client-side check mirroring the server's
// [A-Za-z0-9-] allowlist (internal/mcp/headerparams.go's
// hasNonAllowlistedHeaderNameByte). The server stays authoritative for
// everything else (reserved names, the denylist, auth-header collisions);
// its 400 detail is shown on save, not duplicated here.
const HEADER_NAME_PATTERN = /^[A-Za-z0-9-]+$/

// validateCustomHeaderNames returns a client-side error message for mode
// 'custom': at least one non-empty name is required, and every non-empty
// name must match HEADER_NAME_PATTERN. Returns null when there is nothing
// to block submission on.
export function validateCustomHeaderNames(
  mode: RunAttributionMode,
  names: CustomHeaderNames,
): string | null {
  if (mode !== 'custom') return null

  const trimmed = [names.onBehalfOfHeader, names.sessionRefHeader, names.traceparentHeader].map((n) =>
    n.trim(),
  )
  if (trimmed.every((n) => n === '')) {
    return 'Enter at least one header name'
  }
  for (const name of trimmed) {
    if (name !== '' && !HEADER_NAME_PATTERN.test(name)) {
      return 'Header names may only contain letters, digits, and "-"'
    }
  }
  return null
}
