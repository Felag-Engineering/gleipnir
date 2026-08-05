import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import React from 'react'
import { MemoryRouter } from 'react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AttentionItem } from './AttentionItem'
import type { AttentionItem as Item } from '@/hooks/useAttentionItems'

const navigate = vi.fn()
vi.mock('react-router', async () => {
  const actual = await vi.importActual<typeof import('react-router')>('react-router')
  return { ...actual, useNavigate: () => navigate }
})

function toolInputItem(overrides: Partial<Item> = {}): Item {
  return {
    type: 'tool_input',
    request_id: 'tir-1',
    run_id: 'r1',
    policy_id: 'p1',
    policy_name: 'nightly-deploy',
    tool_name: 'deploy.release',
    message: 'Delete 12 production records?',
    expires_at: new Date(Date.now() + 15 * 60 * 1000).toISOString(),
    created_at: new Date().toISOString(),
    elicitation_kind: 'permission',
    untrusted_message: true,
    sortKey: 0,
    ...overrides,
  }
}

function renderItem(item: Item) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    React.createElement(
      QueryClientProvider,
      { client },
      React.createElement(
        MemoryRouter,
        null,
        React.createElement(AttentionItem, { item, onDismiss: vi.fn() }),
      ),
    ),
  )
}

describe('AttentionItem — tool-initiated requests', () => {
  it('shows the tool and the question', () => {
    renderItem(toolInputItem())

    expect(screen.getByText('TOOL ASK')).toBeInTheDocument()
    expect(screen.getByText('deploy.release')).toBeInTheDocument()
    expect(screen.getByText(/Delete 12 production records\?/)).toBeInTheDocument()
    expect(screen.getByText('nightly-deploy')).toBeInTheDocument()
  })

  // A one-click Approve in the queue would be consent given without having read
  // what was asked — the question, its schema, its deadline source, and any
  // prior attempt only exist on the run detail card.
  it('offers no inline approve or reject', () => {
    renderItem(toolInputItem())

    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Reject' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Review' })).toBeInTheDocument()
  })

  it('labels the action by what the ask is', () => {
    renderItem(toolInputItem({ elicitation_kind: 'information' }))
    expect(screen.getByRole('button', { name: 'Respond' })).toBeInTheDocument()
  })

  it('navigates to the run rather than resolving in place', async () => {
    navigate.mockClear()
    renderItem(toolInputItem())

    await userEvent.click(screen.getByRole('button', { name: 'Review' }))
    expect(navigate).toHaveBeenCalledWith('/runs/r1')
  })

  // Server-controlled text (§6.1): it reaches the DOM as characters.
  it('renders the question as content, not markup', () => {
    const hostile = '<img src=x onerror="alert(1)">'
    renderItem(toolInputItem({ message: hostile }))

    const rendered = screen.getByText(hostile)
    expect(rendered.querySelector('img')).toBeNull()
  })

  // A request whose text the host could not read must still be visible —
  // otherwise a malformed server response hides a paused run entirely.
  it('still points at the run when the question has no readable text', () => {
    renderItem(toolInputItem({ message: '' }))
    expect(screen.getByText(/is asking for a response/)).toBeInTheDocument()
  })
})
