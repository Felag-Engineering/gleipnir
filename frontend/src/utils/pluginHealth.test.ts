import { describe, it, expect } from 'vitest'
import { worstHealth, pluginHealthLabel } from '@/utils/pluginHealth'
import type { PluginHealthState } from '@/api/types'

describe('worstHealth', () => {
  it('returns healthy for an empty array', () => {
    expect(worstHealth([])).toBe('healthy')
  })

  it('returns the single state when only one is provided', () => {
    expect(worstHealth(['unhealthy'])).toBe('unhealthy')
  })

  it('picks the worst across mixed states', () => {
    const states: PluginHealthState[] = ['healthy', 'unhealthy', 'crashed']
    expect(worstHealth(states)).toBe('crashed')
  })

  it('returns healthy when all states are healthy', () => {
    expect(worstHealth(['healthy', 'healthy'])).toBe('healthy')
  })

  it('treats crashed as worse than signature_invalid', () => {
    // crashed = severity 7, signature_invalid = 6, verification_error = 5
    const states: PluginHealthState[] = ['signature_invalid', 'verification_error', 'crashed']
    expect(worstHealth(states)).toBe('crashed')
  })

  it('treats signature_invalid as worse than verification_error', () => {
    const states: PluginHealthState[] = ['verification_error', 'signature_invalid']
    expect(worstHealth(states)).toBe('signature_invalid')
  })

  it('all pending states have equal severity (returns first encountered)', () => {
    // All three pending states share severity 2 — worstHealth picks whichever
    // appears last when tied (iterates in order, replaces on strictly-greater).
    const states: PluginHealthState[] = [
      'pending_key_approval',
      'pending_manifest_approval',
      'pending_config_migration',
    ]
    // None is strictly worse than another; first one stays as worst.
    expect(worstHealth(states)).toBe('pending_key_approval')
  })
})

describe('pluginHealthLabel', () => {
  const cases: Array<[PluginHealthState, string]> = [
    ['healthy',                   'Healthy'],
    ['unsigned_permissive',       'Unsigned (permissive)'],
    ['pending_key_approval',      'Pending key approval'],
    ['pending_manifest_approval', 'Pending manifest approval'],
    ['pending_config_migration',  'Pending config migration'],
    ['unhealthy',                 'Unhealthy'],
    ['circuit_broken',            'Circuit broken'],
    ['verification_error',        'Verification error'],
    ['signature_invalid',         'Signature invalid'],
    ['crashed',                   'Crashed'],
  ]

  for (const [state, label] of cases) {
    it(`${state} → "${label}"`, () => {
      expect(pluginHealthLabel(state)).toBe(label)
    })
  }
})
