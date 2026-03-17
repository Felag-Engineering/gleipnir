import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { PolicyList } from './PolicyList'
import EmptyState from '../../EmptyState/EmptyState'
import type { ApiPolicyListItem } from '../../../api/types'

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

describe('PolicyList', () => {
  it('renders policy name, trigger chip label, and status badge label for a policy with a run', () => {
    render(
      <MemoryRouter>
        <PolicyList policies={[POLICY_WITH_RUN]} onTrigger={() => {}} />
      </MemoryRouter>,
    )

    expect(screen.getByText('vikunja-triage')).toBeInTheDocument()
    expect(screen.getByText('webhook')).toBeInTheDocument()
    expect(screen.getByText('Complete')).toBeInTheDocument()
  })

  it('renders no policy rows for an empty array', () => {
    render(
      <MemoryRouter>
        <PolicyList policies={[]} onTrigger={() => {}} />
      </MemoryRouter>,
    )

    expect(screen.queryByText('vikunja-triage')).toBeNull()
  })

  it('renders the run status area as a link to /runs/:id', () => {
    render(
      <MemoryRouter>
        <PolicyList policies={[POLICY_WITH_RUN]} onTrigger={() => {}} />
      </MemoryRouter>,
    )

    const runLink = screen.getByRole('link', { name: /complete/i })
    expect(runLink).toHaveAttribute('href', '/runs/r101')
  })
})

describe('EmptyState "New Policy" link', () => {
  it('renders the CTA as a link to /policies/new', () => {
    render(
      <MemoryRouter>
        <EmptyState
          headline="No policies yet"
          subtext="Create your first policy to get started."
          ctaLabel="New Policy"
          ctaTo="/policies/new"
        />
      </MemoryRouter>,
    )

    expect(screen.getByText('New Policy')).toBeInTheDocument()
    const link = screen.getByRole('link', { name: 'New Policy' })
    expect(link).toHaveAttribute('href', '/policies/new')
  })
})
