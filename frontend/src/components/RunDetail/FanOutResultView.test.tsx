import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { FanOutResultView } from './FanOutResultView'
import { ALL_OK_24, ANSWER_REFUSED, APPROVED_RETRY, DEMO_HOSTNAMES, DEMO_MIXED, DENIED_RETRY, EMPTY, MIXED_24, SINGLE } from './fanOutFixtures'

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

describe('FanOutResultView — answered approval retry', () => {
  it('shows the decision Relay recorded above the per-Node table', () => {
    render(<FanOutResultView result={APPROVED_RETRY} />)
    expect(screen.getByText('Relay decision')).toBeInTheDocument()
    expect(screen.getByText('approved')).toHaveAttribute('data-tone', 'ok')
    expect(screen.getByText('1 of 1')).toBeInTheDocument()
    expect(screen.getByText('in-band')).toBeInTheDocument()
    expect(screen.getByText('dana')).toBeInTheDocument()
    expect(screen.getByText(/\(asserted\)/)).toBeInTheDocument()
    expect(screen.getByText(/request 172e493bee384ac7eef1aae480536ee9/)).toBeInTheDocument()
    expect(screen.getByText(/^Relay to the agent: the call was re-entered/)).toBeInTheDocument()
    expect(screen.getByText('2 success')).toBeInTheDocument()
    expect(screen.getByRole('table')).toBeInTheDocument()
  })

  it('omits the on-behalf-of clause when Relay recorded no asserted approver', () => {
    const result = { ...APPROVED_RETRY, decision: { ...APPROVED_RETRY.decision!, on_behalf_of: '' } }
    render(<FanOutResultView result={result} />)
    expect(screen.queryByText(/on behalf of/)).toBeNull()
  })

  it('renders no decision line for an ordinary dispatch', () => {
    render(<FanOutResultView result={MIXED_24} />)
    expect(screen.queryByText('Relay decision')).toBeNull()
  })
})

describe('FanOutResultView — hostnames', () => {
  const N4 = 'node-814e5b14eca5206060abdb164672609f'

  it('without a map, shows each node_id as the row label exactly as today', () => {
    const { container } = render(<FanOutResultView result={DEMO_MIXED} />)
    const firstCells = Array.from(container.querySelectorAll('tbody > tr[data-outcome] > td:first-child'))
    // Exceptions first (denied, failed), then successes, in node_id order.
    expect(firstCells.map((td) => td.textContent)).toEqual([
      'node-09671d9abbfdc18526b3bac7e42291d7',
      'node-875ce40155e0f76608693dd55c551a1d',
      'node-1123aea7732badca90d02d21bd1aac96',
      N4,
    ])
    expect(screen.queryByText('dev-node-4')).toBeNull()
  })

  it('with a map, labels each row by hostname and keeps the full node_id beside it', () => {
    const { container } = render(<FanOutResultView result={DEMO_MIXED} hostnames={DEMO_HOSTNAMES} />)
    const row = screen.getByText('dev-node-4').closest('td')!
    expect(row).toHaveAttribute('title', N4)
    expect(row.textContent).toBe(`dev-node-4${N4}`)
    const labels = Array.from(container.querySelectorAll('tbody > tr[data-outcome] > td:first-child'))
      .map((td) => td.firstElementChild?.textContent)
    expect(labels).toEqual(['dev-node-8', 'dev-node-7', 'dev-node-5', 'dev-node-4'])
    // The other labelled rows carry their ids as well.
    expect(screen.getByText('dev-node-8').closest('td')).toHaveAttribute('title', 'node-09671d9abbfdc18526b3bac7e42291d7')
  })

  it('falls back to the node_id for an id the map does not hold', () => {
    const partial = new Map([[N4, 'dev-node-4']])
    const { container } = render(<FanOutResultView result={DEMO_MIXED} hostnames={partial} />)
    const unknownCell = container.querySelector('tr[data-outcome="failure"] > td:first-child')!
    expect(unknownCell.textContent).toBe('node-875ce40155e0f76608693dd55c551a1d')
    expect(unknownCell).not.toHaveAttribute('title')
  })

  it('renders a hostname as text, never as markup', () => {
    const hostile = new Map([[N4, '<img src=x onerror=alert(1)>']])
    const { container } = render(<FanOutResultView result={DEMO_MIXED} hostnames={hostile} />)
    expect(container.querySelector('img')).toBeNull()
    expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeInTheDocument()
  })

  it('repeats the node_id in the expanded detail of a hostname-labelled row', async () => {
    const user = userEvent.setup()
    render(<FanOutResultView result={DEMO_MIXED} hostnames={DEMO_HOSTNAMES} />)
    const row = screen.getByText('dev-node-4').closest('tr')!
    await user.click(row.querySelector('button')!)
    const detail = row.nextElementSibling!
    expect(detail.textContent).toContain(`node_id ${N4}`)
  })
})

describe('FanOutResultView — answered retry that dispatched nothing', () => {
  it('a denial shows the decision as a refusal, Relay\'s next step, and no table', () => {
    const { container } = render(<FanOutResultView result={DENIED_RETRY} />)
    expect(screen.getByText('Relay decision')).toBeInTheDocument()
    expect(screen.getByText('denied')).toHaveAttribute('data-tone', 'failed')
    expect(screen.getByText(/a denial is terminal/)).toBeInTheDocument()
    expect(screen.getByText('Nothing was dispatched.')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()
    expect(container.querySelector('[data-tone="ok"]')).toBeNull()
    expect(screen.queryByText(/^job /)).toBeNull()
    expect(screen.queryByText(/No per-Node results/)).toBeNull()
  })

  it('a refused answer shows Relay\'s reason and next step, and no table', () => {
    render(<FanOutResultView result={ANSWER_REFUSED} />)
    expect(screen.getByText('answer refused')).toHaveAttribute('data-tone', 'failed')
    expect(screen.getByText(ANSWER_REFUSED.answer_refused!.reason)).toBeInTheDocument()
    expect(screen.getByText(/it cannot be decided again/)).toBeInTheDocument()
    expect(screen.getByText('Nothing was dispatched.')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()
  })

  it('an approval still renders as the go-ahead, with its table', () => {
    render(<FanOutResultView result={APPROVED_RETRY} />)
    expect(screen.getByText('approved')).toHaveAttribute('data-tone', 'ok')
    expect(screen.getByRole('table')).toBeInTheDocument()
    expect(screen.queryByText('Nothing was dispatched.')).toBeNull()
  })
})
