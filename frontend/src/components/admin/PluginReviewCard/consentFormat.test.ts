import { describe, it, expect } from 'vitest'
import type { ApiPluginV2Consent } from '@/api/types'
import { diffConsent, elicitationLabel } from './consentFormat'

function consent(
  domains: string[],
  memoryMb: number,
  cpuMillicores: number,
): ApiPluginV2Consent {
  return {
    egress: domains.map((domain) => ({ domain })),
    effective_resources: {
      memory_mb: memoryMb,
      cpu_millicores: cpuMillicores,
      memory_source: 'manifest',
      cpu_source: 'manifest',
    },
  } as ApiPluginV2Consent
}

describe('diffConsent', () => {
  it('reports added and removed egress and changed limits', () => {
    const diff = diffConsent(consent(['a.example', 'b.example'], 256, 500), consent(['b.example', 'c.example'], 512, 500))

    expect(diff.egressAdded).toEqual(['c.example'])
    expect(diff.egressRemoved).toEqual(['a.example'])
    expect(diff.memory?.before.memory_mb).toBe(256)
    expect(diff.memory?.after.memory_mb).toBe(512)
    expect(diff.cpu).toBeNull()
  })

  it('reports nothing when egress and limits match', () => {
    const diff = diffConsent(consent(['a.example'], 256, 500), consent(['a.example'], 256, 500))

    expect(diff).toEqual({ egressAdded: [], egressRemoved: [], memory: null, cpu: null })
  })
})

describe('elicitationLabel', () => {
  it.each([
    [undefined, null],
    ['', null],
    ['permission', 'May ask for your permission'],
    ['information', 'May ask you for information'],
    ['something_new', 'May ask for your input'],
  ])('maps %s', (kind, want) => {
    expect(elicitationLabel(kind)).toBe(want)
  })
})
