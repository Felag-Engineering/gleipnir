import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import React from 'react'
import { RunHeader } from './RunHeader'
import type { ApiRun } from '@/api/types'

const BASE_RUN: ApiRun = {
  id: 'run-1',
  policy_id: 'pol-1',
  policy_name: 'test-policy',
  status: 'complete',
  trigger_type: 'manual',
  trigger_payload: '{}',
  started_at: '2026-01-01T00:00:00Z',
  completed_at: '2026-01-01T00:01:00Z',
  token_cost: 100,
  error: null,
  created_at: '2026-01-01T00:00:00Z',
  system_prompt: null,
  model: 'claude-sonnet-4-6',
}

const TWO_REAL_TOOLS = [
  { server_name: 'fs', tool_name: 'read_file', approval: 'none' as const },
  { server_name: 'fs', tool_name: 'write_file', approval: 'required' as const },
]

function renderHeader(capabilitySnapshot: React.ComponentProps<typeof RunHeader>['capabilitySnapshot']) {
  return render(
    <MemoryRouter>
      <RunHeader
        run={BASE_RUN}
        toolCallCount={0}
        tokenTotal={0}
        duration={60_000}
        capabilitySnapshot={capabilitySnapshot}
      />
    </MemoryRouter>,
  )
}

describe('RunHeader — feedback filtering', () => {
  it('shows "2 tools" when snapshot has 2 real tools + ask_operator entry', () => {
    renderHeader({
      provider: 'anthropic',
      model: 'claude-sonnet-4-6',
      toolCount: 2,
      tools: TWO_REAL_TOOLS,
      feedbackEnabled: true,
    })
    const bar = screen.getByRole('button', { name: /2 tools/i })
    expect(bar.textContent).toContain('2 tools')
    expect(bar.textContent).not.toContain('3 tools')
  })

  it('shows a Feedback chip when feedbackEnabled is true', () => {
    renderHeader({
      provider: 'anthropic',
      model: 'claude-sonnet-4-6',
      toolCount: 2,
      tools: TWO_REAL_TOOLS,
      feedbackEnabled: true,
    })
    expect(screen.getByText('Feedback')).toBeInTheDocument()
  })

  it('does NOT show a Feedback chip when feedbackEnabled is false', () => {
    renderHeader({
      provider: 'anthropic',
      model: 'claude-sonnet-4-6',
      toolCount: 2,
      tools: TWO_REAL_TOOLS,
      feedbackEnabled: false,
    })
    expect(screen.queryByText('Feedback')).not.toBeInTheDocument()
  })

  it('does NOT show a Feedback chip when feedbackEnabled is omitted', () => {
    renderHeader({
      provider: 'anthropic',
      model: 'claude-sonnet-4-6',
      toolCount: 2,
      tools: TWO_REAL_TOOLS,
    })
    expect(screen.queryByText('Feedback')).not.toBeInTheDocument()
  })

  it('the capability summary jumps to the timeline snapshot card instead of expanding a second list', () => {
    const target = document.createElement('section')
    target.id = 'capability-snapshot'
    const scrollIntoView = vi.fn()
    target.scrollIntoView = scrollIntoView
    document.body.appendChild(target)
    try {
      renderHeader({
        provider: 'anthropic',
        model: 'claude-sonnet-4-6',
        toolCount: 2,
        tools: TWO_REAL_TOOLS,
        feedbackEnabled: true,
      })
      fireEvent.click(screen.getByRole('button', { name: /2 tools/i }))
      expect(scrollIntoView).toHaveBeenCalledTimes(1)
      // The header no longer carries its own copy of the tool list.
      expect(screen.queryByRole('table')).not.toBeInTheDocument()
      expect(screen.queryByText('read_file')).not.toBeInTheDocument()
    } finally {
      target.remove()
    }
  })

  it('shows "1 tool" when snapshot has exactly one real tool and no feedback', () => {
    renderHeader({
      toolCount: 1,
      tools: [{ server_name: 'fs', tool_name: 'read_file', approval: 'none' as const }],
      feedbackEnabled: false,
    })
    const bar = screen.getByRole('button', { name: /1 tool/i })
    expect(bar.textContent).toContain('1 tool')
    expect(bar.textContent).not.toContain('tools')
  })
})

describe('RunHeader — status badge for a tool-initiated permission ask', () => {
  function renderWithStatus(status: ApiRun['status'], awaitingPermission?: boolean) {
    return render(
      <MemoryRouter>
        <RunHeader
          run={{ ...BASE_RUN, status, completed_at: null }}
          toolCallCount={0}
          tokenTotal={0}
          duration={1_000}
          awaitingPermission={awaitingPermission}
        />
      </MemoryRouter>,
    )
  }

  it('reads "Awaiting Approval" while parked on a permission ask', () => {
    renderWithStatus('waiting_for_feedback', true)
    expect(screen.getByText('Awaiting Approval')).toBeInTheDocument()
    expect(screen.queryByText('Awaiting Feedback')).not.toBeInTheDocument()
  })

  it('keeps "Awaiting Feedback" for an information ask or native feedback', () => {
    renderWithStatus('waiting_for_feedback', false)
    expect(screen.getByText('Awaiting Feedback')).toBeInTheDocument()
  })

  it('ignores awaitingPermission for any other status', () => {
    renderWithStatus('running', true)
    expect(screen.getByText('Running')).toBeInTheDocument()
  })
})

describe('RunHeader — deleted agent', () => {
  function renderDeleted() {
    return render(
      <MemoryRouter>
        <RunHeader
          run={{ ...BASE_RUN, status: 'failed', policy_deleted: true }}
          toolCallCount={0}
          tokenTotal={0}
          duration={60_000}
          capabilitySnapshot={null}
          showRetry
          onRetry={vi.fn()}
        />
      </MemoryRouter>,
    )
  }

  it('marks the agent name as deleted', () => {
    renderDeleted()
    expect(screen.getByText('test-policy (deleted)')).toBeInTheDocument()
  })

  it('does not offer retry for a deleted agent', () => {
    renderDeleted()
    expect(screen.queryByRole('button', { name: /retry/i })).not.toBeInTheDocument()
  })
})
