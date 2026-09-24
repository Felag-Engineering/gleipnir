import { describe, it, expect } from 'vitest'
import {
  formatRunAttribution,
  buildRunAttributionRequest,
  validateCustomHeaderNames,
} from './runAttribution'
import type { ApiMcpServer } from '@/api/types'

const baseServer: ApiMcpServer = {
  id: 'srv-1',
  name: 'test-server',
  url: 'https://mcp-test-server:8443/mcp',
  last_discovered_at: '2026-04-03T15:44:01Z',
  has_drift: false,
  created_at: '2026-04-03T15:43:55Z',
  is_arcade_gateway: false,
  trust_tier: 'external' as const,
  plugin_instance_id: null,
  editable: true,
  protocol_version: '2026-07-28',
}

describe('formatRunAttribution', () => {
  it('shows "Off" when the field is absent (old fixture)', () => {
    expect(formatRunAttribution(baseServer)).toBe('Off')
  })

  it('shows "Off" for mode off', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      run_attribution: { mode: 'off', on_behalf_of_header: '', session_ref_header: '', traceparent_header: '' },
    }
    expect(formatRunAttribution(server)).toBe('Off')
  })

  it('shows the Relay preset names for mode relay', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      run_attribution: {
        mode: 'relay',
        on_behalf_of_header: 'X-Relay-On-Behalf-Of',
        session_ref_header: 'X-Relay-Session-Ref',
        traceparent_header: 'traceparent',
      },
    }
    expect(formatRunAttribution(server)).toBe(
      'Relay preset: X-Relay-On-Behalf-Of, X-Relay-Session-Ref, traceparent',
    )
  })

  it('shows only the configured names for mode custom', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      run_attribution: {
        mode: 'custom',
        on_behalf_of_header: 'X-Actor',
        session_ref_header: '',
        traceparent_header: '',
      },
    }
    expect(formatRunAttribution(server)).toBe('Custom headers: X-Actor')
  })
})

describe('buildRunAttributionRequest', () => {
  const emptyNames = { onBehalfOfHeader: '', sessionRefHeader: '', traceparentHeader: '' }

  it('off gives {mode: "off"}', () => {
    expect(buildRunAttributionRequest('off', emptyNames)).toEqual({ mode: 'off' })
  })

  it('relay gives {mode: "relay"} with no names, even if names were supplied', () => {
    expect(
      buildRunAttributionRequest('relay', { ...emptyNames, onBehalfOfHeader: 'X-Ignored' }),
    ).toEqual({ mode: 'relay' })
  })

  it('custom includes only the trimmed, non-empty names', () => {
    expect(
      buildRunAttributionRequest('custom', {
        onBehalfOfHeader: '  X-Actor  ',
        sessionRefHeader: '',
        traceparentHeader: 'traceparent',
      }),
    ).toEqual({ mode: 'custom', on_behalf_of_header: 'X-Actor', traceparent_header: 'traceparent' })
  })
})

describe('validateCustomHeaderNames', () => {
  it('is a no-op for off and relay', () => {
    const names = { onBehalfOfHeader: '', sessionRefHeader: '', traceparentHeader: '' }
    expect(validateCustomHeaderNames('off', names)).toBeNull()
    expect(validateCustomHeaderNames('relay', names)).toBeNull()
  })

  it('requires at least one name for custom', () => {
    const names = { onBehalfOfHeader: '', sessionRefHeader: '', traceparentHeader: '' }
    expect(validateCustomHeaderNames('custom', names)).toMatch(/at least one/i)
  })

  it('accepts a single valid name', () => {
    const names = { onBehalfOfHeader: 'X-Actor', sessionRefHeader: '', traceparentHeader: '' }
    expect(validateCustomHeaderNames('custom', names)).toBeNull()
  })

  it('rejects a name with an invalid character', () => {
    const names = { onBehalfOfHeader: 'X_Actor', sessionRefHeader: '', traceparentHeader: '' }
    expect(validateCustomHeaderNames('custom', names)).toMatch(/letters, digits/i)
  })
})
