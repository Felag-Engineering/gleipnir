import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import React from 'react'
import type { ApiRunStep } from '@/api/types'
import { parseStep } from './types'
import { ThoughtBlock, collapseText } from './ThoughtBlock'

function thought(text: string) {
  const raw: ApiRunStep = {
    id: 'step-1',
    run_id: 'run-1',
    step_number: 0,
    type: 'thought',
    content: JSON.stringify({ text }),
    token_cost: 0,
    created_at: '2026-10-01T12:00:00Z',
  }
  return parseStep(raw) as ReturnType<typeof parseStep> & { type: 'thought' }
}

const report = [
  'Approved in Relay and executed as Job 22e4d38c. Per-Node outcomes:',
  '',
  '- **success** (2): dev-node-4, dev-node-5',
  '- **failure** (1): dev-node-7 — spawn systemctl: No such file or directory',
  '- **denied_by_policy** (1): dev-node-8',
].join('\n')

describe('collapseText', () => {
  const cases: { name: string; input: string; want: string }[] = [
    {
      name: 'cuts at the last line break so bold markers are never split',
      input: report,
      want: [
        'Approved in Relay and executed as Job 22e4d38c. Per-Node outcomes:',
        '',
        '- **success** (2): dev-node-4, dev-node-5',
        '- **failure** (1): dev-node-7 — spawn systemctl: No such file or directory',
      ].join('\n') + '...',
    },
    {
      name: 'falls back to the last space when there is no late line break',
      input: 'word '.repeat(60),
      want: 'word '.repeat(39) + 'word...',
    },
    {
      name: 'hard-cuts a single unbroken token',
      input: 'x'.repeat(300),
      want: 'x'.repeat(200) + '...',
    },
  ]
  for (const c of cases) {
    it(c.name, () => {
      expect(collapseText(c.input)).toBe(c.want)
    })
  }
})

describe('ThoughtBlock', () => {
  it('collapses a long thought by default', () => {
    render(React.createElement(ThoughtBlock, { step: thought(report) }))
    expect(screen.getByRole('button', { name: 'Show more' })).toBeInTheDocument()
    expect(screen.queryByText(/dev-node-8/)).not.toBeInTheDocument()
  })

  it('opens expanded when it is the final answer', () => {
    render(React.createElement(ThoughtBlock, { step: thought(report), defaultExpanded: true }))
    expect(screen.getByRole('button', { name: 'Show less' })).toBeInTheDocument()
    expect(screen.getByText(/dev-node-8/)).toBeInTheDocument()
  })
})
