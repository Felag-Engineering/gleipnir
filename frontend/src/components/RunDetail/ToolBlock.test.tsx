import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ApiRunStep } from '@/api/types'
import { asToolOutput, MIXED_24 } from './fanOutFixtures'
import { parseStep } from './types'
import type { ToolBlockData } from './types'
import { ToolBlock } from './ToolBlock'

function makeRaw(overrides: Partial<ApiRunStep> = {}): ApiRunStep {
  return {
    id: 'step-1',
    run_id: 'run-1',
    step_number: 0,
    type: 'thought',
    content: '{}',
    token_cost: 0,
    created_at: '2026-03-10T12:00:00Z',
    ...overrides,
  }
}

function makeBlock(output: string, isError = false): ToolBlockData {
  return {
    approval: null,
    call: parseStep(makeRaw({
      id: 'step-call',
      type: 'tool_call',
      content: JSON.stringify({
        tool_name: 'relay.run_operation',
        server_id: 'relay-server',
        input: {},
      }),
    })) as ToolBlockData['call'],
    result: parseStep(makeRaw({
      id: 'step-result',
      type: 'tool_result',
      content: JSON.stringify({
        tool_name: 'relay.run_operation',
        output,
        is_error: isError,
      }),
    })) as ToolBlockData['result'],
  }
}

function renderBlock(block: ToolBlockData) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={queryClient}>
      <ToolBlock block={block} runId="run-1" runStatus="complete" />
    </QueryClientProvider>,
  )
}

describe('ToolBlock — unrecognized shapes render in full, exactly as today', () => {
  const planResponse = { results: [], plan: { steps: ['restart'] } }
  const fanOutWithExtraKey = {
    job_id: 'job-1',
    results: [
      {
        node_id: 'node-01',
        outcome: 'success',
        stdout: 'ok',
        stderr: '',
        stdout_truncated: false,
        stderr_truncated: false,
        duration_ms: 100,
        new_field: 'x',
      },
    ],
  }

  it.each([
    ['a plan response', asToolOutput(planResponse), JSON.stringify(planResponse)],
    ['a fan-out object with an extra row key', asToolOutput(fanOutWithExtraKey), JSON.stringify(fanOutWithExtraKey)],
    ['plain text', 'INFO ready', 'INFO ready'],
  ])('%s: no table, no raw toggle, full text preserved', (_label, output, expectedText) => {
    const { container } = renderBlock(makeBlock(output))
    expect(screen.queryByRole('table')).toBeNull()
    expect(screen.queryByRole('button', { name: /show raw output/i })).toBeNull()
    const pre = container.querySelector('pre')
    expect(pre?.textContent).toBe(expectedText)
  })

  it('a text + image envelope still renders CollapsibleJSON', () => {
    const output = JSON.stringify([{ type: 'text', text: 'Saved' }, { type: 'image', data: 'abc' }])
    renderBlock(makeBlock(output))
    expect(screen.queryByRole('table')).toBeNull()
    // CollapsibleJSON renders the value as pretty-printed JSON inside a <code>.
    expect(screen.getByText(/"type": "text"/)).toBeInTheDocument()
    expect(screen.getByText(/"type": "image"/)).toBeInTheDocument()
  })
})

describe('ToolBlock — recognized fan-out shape', () => {
  it('renders a table', () => {
    renderBlock(makeBlock(asToolOutput(MIXED_24)))
    expect(screen.getByRole('table')).toBeInTheDocument()
  })

  it('"Show raw output" swaps to the original JSON string, and "Show table" restores the table', async () => {
    const user = userEvent.setup()
    const raw = asToolOutput(MIXED_24)
    const { container } = renderBlock(makeBlock(raw))

    const toggle = screen.getByRole('button', { name: /show raw output/i })
    await user.click(toggle)

    expect(screen.queryByRole('table')).toBeNull()
    const pre = container.querySelector('pre')
    expect(pre?.textContent).toBe(JSON.stringify(MIXED_24))

    const restoreToggle = screen.getByRole('button', { name: /show table/i })
    await user.click(restoreToggle)
    expect(screen.getByRole('table')).toBeInTheDocument()
  })
})

describe('ToolBlock — is_error: true', () => {
  it('renders the error pane unchanged for a plain error string', () => {
    const { container } = renderBlock(makeBlock('permission denied: /tmp/report.txt', true))
    expect(screen.queryByRole('table')).toBeNull()
    expect(screen.queryByRole('button', { name: /show raw output/i })).toBeNull()
    const pre = container.querySelector('pre')
    expect(pre?.textContent).toBe('permission denied: /tmp/report.txt')
  })
})
