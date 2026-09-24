import { describe, it, expect } from 'vitest'
import { parseCallTimeoutInput, formatCallTimeout } from './callTimeout'
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

describe('parseCallTimeoutInput', () => {
  const cases: { raw: string; wantValue: number | null; wantError: boolean }[] = [
    { raw: '', wantValue: null, wantError: false },
    { raw: '  ', wantValue: null, wantError: false },
    { raw: '120', wantValue: 120, wantError: false },
    { raw: '1', wantValue: 1, wantError: false },
    { raw: '600', wantValue: 600, wantError: false },
    { raw: '0', wantValue: null, wantError: true },
    { raw: '601', wantValue: null, wantError: true },
    { raw: '-1', wantValue: null, wantError: true },
    { raw: '1.5', wantValue: null, wantError: true },
    { raw: 'abc', wantValue: null, wantError: true },
  ]

  for (const tc of cases) {
    it(`parses ${JSON.stringify(tc.raw)}`, () => {
      const result = parseCallTimeoutInput(tc.raw)
      expect(result.value).toBe(tc.wantValue)
      if (tc.wantError) {
        expect(result.error).not.toBeNull()
      } else {
        expect(result.error).toBeNull()
      }
    })
  }
})

describe('formatCallTimeout', () => {
  it('shows the override in seconds when set', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      call_timeout_seconds: 120,
      effective_call_timeout_seconds: 120,
    }
    expect(formatCallTimeout(server)).toBe('120s')
  })

  it('shows "Default (Ns)" when unset and an effective value is known', () => {
    const server: ApiMcpServer = {
      ...baseServer,
      call_timeout_seconds: null,
      effective_call_timeout_seconds: 30,
    }
    expect(formatCallTimeout(server)).toBe('Default (30s)')
  })

  it('falls back to bare "Default" for an old fixture with no effective value', () => {
    expect(formatCallTimeout(baseServer)).toBe('Default')
  })
})
