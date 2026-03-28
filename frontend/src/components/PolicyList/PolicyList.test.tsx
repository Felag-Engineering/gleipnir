import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { PolicyList } from './PolicyList'
import type { ApiPolicyListItem } from '@/api/types'

const BASE_POLICY: ApiPolicyListItem = {
  id: 'p1',
  name: 'vikunja-triage',
  trigger_type: 'webhook',
  folder: '',
  created_at: '2026-03-07T14:32:11Z',
  updated_at: '2026-03-07T14:32:11Z',
  paused_at: null,
  latest_run: null,
}

const POLICY_WITH_RUN: ApiPolicyListItem = {
  ...BASE_POLICY,
  latest_run: {
    id: 'r101',
    status: 'complete',
    started_at: '2026-03-07T14:32:11Z',
    token_cost: 8420,
  },
}

const POLICY_WITH_FOLDER: ApiPolicyListItem = {
  ...BASE_POLICY,
  id: 'p2',
  name: 'deploy-agent',
  folder: 'CI/CD',
}

describe('PolicyList', () => {
  it('renders policy name, trigger chip, and status badge for a policy with a run', () => {
    render(
      <MemoryRouter>
        <PolicyList policies={[POLICY_WITH_RUN]} onTrigger={() => {}} />
      </MemoryRouter>,
    )

    expect(screen.getByText('vikunja-triage')).toBeInTheDocument()
    expect(screen.getByText('webhook')).toBeInTheDocument()
    expect(screen.getByText('Complete')).toBeInTheDocument()
  })

  it('renders no rows for empty array', () => {
    render(
      <MemoryRouter>
        <PolicyList policies={[]} onTrigger={() => {}} />
      </MemoryRouter>,
    )

    expect(screen.queryByText('vikunja-triage')).toBeNull()
  })

  it('groups by folder when groupByFolder=true', () => {
    render(
      <MemoryRouter>
        <PolicyList
          policies={[BASE_POLICY, POLICY_WITH_FOLDER]}
          groupByFolder={true}
          onTrigger={() => {}}
        />
      </MemoryRouter>,
    )

    expect(screen.getByText('Default')).toBeInTheDocument()
    expect(screen.getByText('CI/CD')).toBeInTheDocument()
  })

  it('renders flat list when groupByFolder=false', () => {
    render(
      <MemoryRouter>
        <PolicyList
          policies={[BASE_POLICY, POLICY_WITH_FOLDER]}
          groupByFolder={false}
          onTrigger={() => {}}
        />
      </MemoryRouter>,
    )

    expect(screen.queryByText('Default')).toBeNull()
    expect(screen.queryByText('CI/CD')).toBeNull()
    expect(screen.getByText('vikunja-triage')).toBeInTheDocument()
    expect(screen.getByText('deploy-agent')).toBeInTheDocument()
  })

  it('shows play button when onTrigger is provided', () => {
    render(
      <MemoryRouter>
        <PolicyList policies={[BASE_POLICY]} onTrigger={() => {}} />
      </MemoryRouter>,
    )

    expect(screen.getByRole('button', { name: /run vikunja-triage/i })).toBeInTheDocument()
  })

  it('hides play button when onTrigger is omitted', () => {
    render(
      <MemoryRouter>
        <PolicyList policies={[BASE_POLICY]} />
      </MemoryRouter>,
    )

    expect(screen.queryByRole('button', { name: /run vikunja-triage/i })).toBeNull()
  })

  it('uses renderRunCell when provided', () => {
    const renderRunCell = vi.fn(() => <span>custom-cell</span>)

    render(
      <MemoryRouter>
        <PolicyList policies={[BASE_POLICY]} renderRunCell={renderRunCell} />
      </MemoryRouter>,
    )

    expect(renderRunCell).toHaveBeenCalledWith(BASE_POLICY)
    expect(screen.getByText('custom-cell')).toBeInTheDocument()
  })

  it('row links to /policies/:id when linkTo="editor"', () => {
    render(
      <MemoryRouter>
        <PolicyList policies={[BASE_POLICY]} linkTo="editor" />
      </MemoryRouter>,
    )

    const link = screen.getByRole('link', { name: /vikunja-triage/i })
    expect(link).toHaveAttribute('href', '/policies/p1')
  })

  it('row links to /policies/:id/runs when linkTo="runs" (default)', () => {
    render(
      <MemoryRouter>
        <PolicyList policies={[BASE_POLICY]} />
      </MemoryRouter>,
    )

    const link = screen.getByRole('link', { name: /vikunja-triage/i })
    expect(link).toHaveAttribute('href', '/policies/p1/runs')
  })
})
