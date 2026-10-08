import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { ArgEnforcementBadge, explainArgEnforcement } from './ArgEnforcementBadge'

const REDUCED = ['no_schema', 'no_canonical_schema', 'schema_uncompilable'] as const

describe('ArgEnforcementBadge', () => {
  it('renders nothing for exact or missing enforcement', () => {
    expect(render(<ArgEnforcementBadge state="exact" />).container.firstChild).toBeNull()
    expect(render(<ArgEnforcementBadge state={undefined} />).container.firstChild).toBeNull()
  })

  it('renders a chip for every reduced state, with a distinct reason each', () => {
    const reasons = new Set<string>()
    for (const state of REDUCED) {
      const { unmount } = render(<ArgEnforcementBadge state={state} />)
      const reason = explainArgEnforcement(state)!.reason
      expect(screen.getByText('Reduced argument checking').getAttribute('title')).toContain(
        `Argument checking: reduced — ${reason}`,
      )
      reasons.add(reason)
      unmount()
    }
    expect(reasons.size).toBe(REDUCED.length)
  })

  it('does not expose protocol-specific terms', () => {
    for (const state of REDUCED) {
      const { unmount } = render(<ArgEnforcementBadge state={state} />)
      expect(screen.getByText('Reduced argument checking').getAttribute('title')).not.toMatch(/MCP/i)
      unmount()
    }
  })
})
