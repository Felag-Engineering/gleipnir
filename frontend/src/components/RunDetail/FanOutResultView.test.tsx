import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { FanOutResultView } from './FanOutResultView'
import { ALL_OK_24, EMPTY, MIXED_24, SINGLE } from './fanOutFixtures'

describe('FanOutResultView — all ok', () => {
  it('shows "24 Nodes" and "24 success"', () => {
    render(<FanOutResultView result={ALL_OK_24} />)
    expect(screen.getByText('24 Nodes')).toBeInTheDocument()
    expect(screen.getByText('24 success')).toBeInTheDocument()
  })

  it('renders 24 body rows plus the header row', () => {
    render(<FanOutResultView result={ALL_OK_24} />)
    expect(screen.getAllByRole('row')).toHaveLength(25)
  })

  it('has no policy or failed tones', () => {
    const { container } = render(<FanOutResultView result={ALL_OK_24} />)
    expect(container.querySelector('[data-tone="policy"]')).toBeNull()
    expect(container.querySelector('[data-tone="failed"]')).toBeNull()
  })
})

describe('FanOutResultView — mixed outcomes', () => {
  it('shows a chip count per outcome', () => {
    render(<FanOutResultView result={MIXED_24} />)
    expect(screen.getByText('19 success')).toBeInTheDocument()
    expect(screen.getByText('2 denied by policy')).toBeInTheDocument()
    expect(screen.getByText('1 unreachable')).toBeInTheDocument()
    expect(screen.getByText('1 failed')).toBeInTheDocument()
    expect(screen.getByText('1 version refused')).toBeInTheDocument()
  })

  it('orders body rows: denied → failure → unreachable → version_refused → success', () => {
    const { container } = render(<FanOutResultView result={MIXED_24} />)
    const bodyRows = container.querySelectorAll('tbody > tr[data-outcome]')
    const outcomes = Array.from(bodyRows).map((r) => r.getAttribute('data-outcome'))
    expect(outcomes).toEqual([
      'denied_by_policy',
      'denied_by_policy',
      'failure',
      'unreachable',
      'version_refused',
      ...Array(19).fill('success'),
    ])
  })

  it('gives the denied badge data-tone="policy" and the failure badge data-tone="failed"', () => {
    const { container } = render(<FanOutResultView result={MIXED_24} />)
    const deniedRow = container.querySelector('tr[data-outcome="denied_by_policy"]')
    const failureRow = container.querySelector('tr[data-outcome="failure"]')
    expect(deniedRow?.querySelector('[data-tone]')).toHaveAttribute('data-tone', 'policy')
    expect(failureRow?.querySelector('[data-tone]')).toHaveAttribute('data-tone', 'failed')
  })

  it('shows "—" for exit on denied/unreachable rows and the code on the failure row', () => {
    const { container } = render(<FanOutResultView result={MIXED_24} />)
    const deniedRow = container.querySelector('tr[data-outcome="denied_by_policy"]')
    const unreachableRow = container.querySelector('tr[data-outcome="unreachable"]')
    const failureRow = container.querySelector('tr[data-outcome="failure"]')
    expect(deniedRow?.textContent).toContain('—')
    expect(unreachableRow?.textContent).toContain('—')
    expect(failureRow?.textContent).toContain('1')
  })

  it('orders summary chips: success first, then the rest in OUTCOME_ORDER', () => {
    const { container } = render(<FanOutResultView result={MIXED_24} />)
    const summaryEl = container.querySelector('table')!.previousElementSibling as HTMLElement
    const chipTexts = Array.from(summaryEl.querySelectorAll('[data-tone]')).map((el) => el.textContent)
    expect(chipTexts).toEqual([
      '19 success',
      '2 denied by policy',
      '1 failed',
      '1 unreachable',
      '1 version refused',
    ])
  })

  it('shows both stderr and refusal_explanation when a denied row is expanded', async () => {
    const user = userEvent.setup()
    render(<FanOutResultView result={MIXED_24} />)
    const [firstDeniedButton] = screen.getAllByRole('button', { name: /refused: destructive operation/ })
    await user.click(firstDeniedButton)
    expect(screen.getByText(/Policy "no-destructive-ops" blocks raw_exec/)).toBeInTheDocument()
    expect(screen.getAllByText(/refused: destructive operation blocked by policy/).length).toBeGreaterThan(0)
  })
})

describe('FanOutResultView — single node', () => {
  it('shows "1 Node" (singular) and one body row', () => {
    const { container } = render(<FanOutResultView result={SINGLE} />)
    expect(screen.getByText('1 Node')).toBeInTheDocument()
    expect(container.querySelectorAll('tbody > tr[data-outcome]')).toHaveLength(1)
  })

  it('expands to reveal the full stdout and the truncation note', async () => {
    const user = userEvent.setup()
    render(<FanOutResultView result={SINGLE} />)
    const button = screen.getByRole('button', { expanded: false })
    await user.click(button)
    expect(button).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText(/Truncated by Relay at 4 KiB/)).toBeInTheDocument()
    expect(screen.getByText(/log line 49/)).toBeInTheDocument()
  })
})

describe('FanOutResultView — empty result', () => {
  it('shows "0 Nodes", the job id, and the empty-state sentence, with no table', () => {
    render(<FanOutResultView result={EMPTY} />)
    expect(screen.getByText('0 Nodes')).toBeInTheDocument()
    expect(screen.getByText(/job-empty/)).toBeInTheDocument()
    expect(screen.getByText('No per-Node results were returned for this job.')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()
  })
})
