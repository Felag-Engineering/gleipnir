import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { ProtocolBadge, protocolState } from './ProtocolBadge'
import type { ProtocolState } from './ProtocolBadge'

describe('protocolState', () => {
  it('maps each version to the expected state', () => {
    const cases: Array<[string | null | undefined, ProtocolState]> = [
      ['2026-07-28', 'modern'],
      ['2024-11-05', 'legacy'],
      ['2025-03-26', 'legacy'],
      ['2025-06-18', 'legacy'],
      ['2027-01-01', 'legacy'], // unrecognized non-null fails safe, never 'modern'
      [null, 'unknown'],
      [undefined, 'unknown'],
      ['', 'unknown'],
    ]
    for (const [version, expected] of cases) {
      expect(protocolState(version)).toBe(expected)
    }
  })
})

describe('ProtocolBadge', () => {
  it('renders the modern label and variant', () => {
    render(<ProtocolBadge version="2026-07-28" />)
    const badge = screen.getByText('Protocol 2026-07-28')
    expect(badge.className).toMatch(/modern/)
    expect(badge).toHaveAttribute(
      'title',
      'Pinned protocol revision (2026-07-28) — the revision Gleipnir negotiates with this source.',
    )
  })

  it('renders the legacy label and variant', () => {
    render(<ProtocolBadge version="2024-11-05" />)
    const badge = screen.getByText('Legacy protocol')
    expect(badge.className).toMatch(/legacy/)
    expect(badge).toHaveAttribute(
      'title',
      'Pinned protocol revision (2024-11-05) — this source is on a legacy revision.',
    )
  })

  it('renders the unknown label and variant', () => {
    render(<ProtocolBadge version={null} />)
    const badge = screen.getByText('Protocol unknown')
    expect(badge.className).toMatch(/unknown/)
    expect(badge.getAttribute('title')).toContain('Rediscover')
  })
})
