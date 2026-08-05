import { describe, it, expect, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import React from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import { ToolInputCard } from './ToolInputCard'
import type { ApiToolInputRequest } from '@/api/types'

function renderCard(request: ApiToolInputRequest, roles: string[] = ['approver', 'operator']) {
  server.use(
    http.get('/api/v1/auth/me', () =>
      HttpResponse.json({ data: { id: 'u1', username: 'op', roles } }),
    ),
  )
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    React.createElement(
      QueryClientProvider,
      { client },
      React.createElement(ToolInputCard, { request }),
    ),
  )
}

function permissionRequest(overrides: Partial<ApiToolInputRequest> = {}): ApiToolInputRequest {
  return {
    id: 'tir-1',
    run_id: 'r1',
    tool_name: 'deploy.release',
    elicitation_kind: 'permission',
    required_role: 'approver',
    expires_at: new Date(Date.now() + 20 * 60 * 1000).toISOString(),
    deadline_source: 'policy',
    created_at: new Date().toISOString(),
    requests: [{ message: 'Delete 12 production records?' }],
    untrusted_content: true,
    ...overrides,
  }
}

function informationRequest(overrides: Partial<ApiToolInputRequest> = {}): ApiToolInputRequest {
  return permissionRequest({
    elicitation_kind: 'information',
    required_role: 'operator',
    requests: [
      {
        message: 'Which region should the rollout target?',
        requested_schema: {
          type: 'object',
          properties: { region: { type: 'string', title: 'Region' } },
        },
      },
    ],
    ...overrides,
  })
}

describe('ToolInputCard', () => {
  beforeEach(() => {
    server.resetHandlers()
  })

  // The §6.1 split is not cosmetic: one is an authorization decision, the other
  // is data entry, and they are answerable by different roles.
  it('renders a permission ask as approve/reject with no form', async () => {
    renderCard(permissionRequest())

    expect(await screen.findByText('PERMISSION')).toBeInTheDocument()
    expect(screen.getByText('Delete 12 production records?')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Approve' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Reject' })).toBeInTheDocument()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  })

  it('renders an information ask as a form built from the requested schema', async () => {
    renderCard(informationRequest())

    expect(await screen.findByText('INFORMATION')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Submit' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Decline' })).toBeInTheDocument()
    expect(screen.getByLabelText(/region/i)).toBeInTheDocument()
  })

  // Elicitation text is server-controlled (§6.1). It must reach the DOM as
  // characters, never as markup — a server that can inject an element into an
  // operator's console is a server that can forge the console.
  it('renders server text as content, never as markup', async () => {
    const hostile = '<img src=x onerror="alert(1)"> **not bold** <script>alert(2)</script>'
    renderCard(permissionRequest({ requests: [{ message: hostile }] }))

    // The message element holds the string as a single text node and nothing
    // else: no tags were parsed out of it, and markdown emphasis stays literal
    // because this text is not run through a renderer anywhere.
    const rendered = await screen.findByText(hostile)
    expect(rendered.childElementCount).toBe(0)
    expect(rendered.textContent).toBe(hostile)
  })

  // URL mode (§6.1): an explicit "open in browser" step with the destination
  // called out. Never auto-opened, never framed.
  it('surfaces a URL as an explicit browser step with the host shown', async () => {
    renderCard(
      permissionRequest({
        requests: [{ message: 'Approve at https://auth.example.com/consent?id=7' }],
      }),
    )

    const anchor = await screen.findByRole('link', { name: 'Open in browser' })
    expect(anchor).toHaveAttribute('href', 'https://auth.example.com/consent?id=7')
    expect(anchor).toHaveAttribute('target', '_blank')
    expect(anchor.getAttribute('rel')).toContain('noopener')
    expect(screen.getByText('auth.example.com')).toBeInTheDocument()
  })

  it('does not offer a browser step for a non-navigable scheme', async () => {
    renderCard(permissionRequest({ requests: [{ message: 'Click javascript:alert(1)' }] }))

    expect(await screen.findByText('Click javascript:alert(1)')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Open in browser' })).not.toBeInTheDocument()
  })

  // The deadline is the minimum of three clocks (§6.3), and which one won
  // changes what running out means.
  it.each([
    ['policy', /Gleipnir's own timeout/i],
    ['server_ttl', /task expiry/i],
    ['request_state', /replays your answer automatically/i],
  ])('explains a %s deadline', async (source, pattern) => {
    renderCard(permissionRequest({ deadline_source: source }))
    expect(await screen.findByText(pattern)).toBeInTheDocument()
  })

  it('shows the countdown to the effective deadline', async () => {
    renderCard(permissionRequest())
    expect(await screen.findByText(/^\d+:\d{2}$/)).toBeInTheDocument()
  })

  // A second prompt that looks like a duplicate but is not is exactly where a
  // reflexive approval does the most damage (§6.5).
  it('shows the prior question and answer when the server re-asked differently', async () => {
    renderCard(
      permissionRequest({
        prior_attempt: {
          reason: 'the tool re-asked a different question after your answer',
          prior_questions: [{ message: 'Delete 3 staging records?' }],
          prior_answers: [{ action: 'accept' }],
        },
      }),
    )

    expect(await screen.findByText('You already answered a different question')).toBeInTheDocument()
    expect(screen.getByText('Delete 3 staging records?')).toBeInTheDocument()
    expect(screen.getByText('accept')).toBeInTheDocument()
  })

  // Reading what a run is blocked on is not the same authority as answering
  // it. An auditor who cannot see the question cannot audit the decision.
  it('shows the question but no controls to someone who cannot answer', async () => {
    renderCard(permissionRequest(), ['auditor'])

    expect(await screen.findByText('Delete 12 production records?')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
    expect(screen.getByText(/needs the/i)).toBeInTheDocument()
    expect(screen.getByText('approver')).toBeInTheDocument()
  })

  // An operator may supply values but may not grant permission — the gate the
  // backend enforces per row, mirrored so the button is not one that only
  // fails when pressed.
  it('withholds approve/reject from an operator on a permission ask', async () => {
    renderCard(permissionRequest(), ['operator'])

    expect(await screen.findByText(/needs the/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Approve' })).not.toBeInTheDocument()
  })

  it('lets an operator answer an information ask', async () => {
    renderCard(informationRequest(), ['operator'])
    expect(await screen.findByRole('button', { name: 'Submit' })).toBeInTheDocument()
  })

  it('posts one response per question, correlated by position', async () => {
    let body: unknown
    server.use(
      http.post('/api/v1/runs/r1/tool-input', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({ data: { run_id: 'r1', request_id: 'tir-1' } }, { status: 202 })
      }),
    )
    renderCard(
      permissionRequest({
        requests: [{ message: 'First?' }, { message: 'Second?' }],
      }),
    )

    await userEvent.click(await screen.findByRole('button', { name: 'Approve' }))

    await waitFor(() => expect(body).toBeDefined())
    expect(body).toEqual({
      responses: [
        { action: 'accept', content: { confirmed: true } },
        { action: 'accept', content: { confirmed: true } },
      ],
    })
  })

  // A decline is a real answer, handed back to the server, not a cancellation.
  it('sends a decline with no content', async () => {
    let body: unknown
    server.use(
      http.post('/api/v1/runs/r1/tool-input', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({ data: { run_id: 'r1', request_id: 'tir-1' } }, { status: 202 })
      }),
    )
    renderCard(permissionRequest())

    await userEvent.click(await screen.findByRole('button', { name: 'Reject' }))

    await waitFor(() => expect(body).toBeDefined())
    expect(body).toEqual({ responses: [{ action: 'decline' }] })
  })

  it('sends the operator’s form values on an information ask', async () => {
    let body: unknown
    server.use(
      http.post('/api/v1/runs/r1/tool-input', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({ data: { run_id: 'r1', request_id: 'tir-1' } }, { status: 202 })
      }),
    )
    renderCard(informationRequest(), ['operator'])

    await userEvent.type(await screen.findByLabelText(/region/i), 'eu-west-1')
    await userEvent.click(screen.getByRole('button', { name: 'Submit' }))

    await waitFor(() => expect(body).toBeDefined())
    expect(body).toEqual({
      responses: [{ action: 'accept', content: { region: 'eu-west-1' } }],
    })
  })

  it('surfaces a failed submission rather than looking answered', async () => {
    server.use(
      http.post('/api/v1/runs/r1/tool-input', () =>
        HttpResponse.json({ error: 'gone' }, { status: 410 }),
      ),
    )
    renderCard(permissionRequest())

    await userEvent.click(await screen.findByRole('button', { name: 'Approve' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not submit/i)
  })
})
