import { describe, it, expect } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
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

  it('expanded table omits ask_operator — only shows the 2 real tools', async () => {
    renderHeader({
      provider: 'anthropic',
      model: 'claude-sonnet-4-6',
      toolCount: 2,
      tools: TWO_REAL_TOOLS,
      feedbackEnabled: true,
    })
    fireEvent.click(screen.getByRole('button', { name: /2 tools/i }))
    await waitFor(() => {
      expect(screen.getByRole('table')).toBeInTheDocument()
    })
    // Both real tools are present
    expect(screen.getByText('read_file')).toBeInTheDocument()
    expect(screen.getByText('write_file')).toBeInTheDocument()
    // ask_operator must not appear in the table
    expect(screen.queryByText('ask_operator')).not.toBeInTheDocument()
    expect(screen.queryByText('gleipnir')).not.toBeInTheDocument()
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
