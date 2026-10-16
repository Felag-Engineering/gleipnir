import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'

import { StartupFaultBanner } from './StartupFaultBanner'
import type { ApiPluginInstanceStartupFault } from '@/api/types'

const BASE: ApiPluginInstanceStartupFault = {
  failures: 3,
  last_error: 'start container: open sysctl net.ipv6.conf.all.disable_ipv6: no such file or directory',
  last_generation: 3,
  last_failed_at: '2026-10-01T12:00:00Z',
  next_attempt_at: '2026-10-01T12:00:40Z',
  attempt_in_progress: false,
  serving_generation: null,
}

const BEFORE_NEXT = new Date('2026-10-01T12:00:10Z')
const AFTER_NEXT = new Date('2026-10-01T12:05:00Z')

describe('StartupFaultBanner', () => {
  it.each([
    {
      name: 'nothing serving reads as failed to start',
      fault: BASE,
      now: BEFORE_NEXT,
      title: 'Plugin container failed to start',
      meta: /3 consecutive attempts failed, most recently generation 3 .*Next attempt at /,
    },
    {
      name: 'a serving generation reads as a failing upgrade, not down',
      fault: { ...BASE, failures: 1, last_generation: 5, serving_generation: 4 },
      now: BEFORE_NEXT,
      title: 'Upgrade failing to start — generation 4 is still serving',
      meta: /^1 attempt failed, most recently generation 5 /,
    },
    {
      name: 'an expired backoff says the next attempt is due',
      fault: BASE,
      now: AFTER_NEXT,
      title: 'Plugin container failed to start',
      meta: /The next attempt is due now\.$/,
    },
    {
      name: 'a retry in flight says so instead of a time',
      fault: { ...BASE, attempt_in_progress: true },
      now: BEFORE_NEXT,
      title: 'Plugin container failed to start',
      meta: /A new attempt is in progress\.$/,
    },
  ])('$name', ({ fault, now, title, meta }) => {
    render(<StartupFaultBanner fault={fault} now={now} />)
    const banner = screen.getByRole('status', { name: 'Container startup' })
    expect(banner).toHaveTextContent(title)
    expect(screen.getByText(fault.last_error)).toBeInTheDocument()
    expect(banner.querySelector('p:last-of-type')?.textContent).toMatch(meta)
  })

  it('renders the error as text, never markup', () => {
    render(<StartupFaultBanner fault={{ ...BASE, last_error: '<b>injected</b>' }} now={BEFORE_NEXT} />)
    expect(screen.getByText('<b>injected</b>')).toBeInTheDocument()
  })
})
