import { describe, it, expect } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import type { ApiUserIdentity } from '@/api/types'
import { IdentityFields } from './IdentityFields'

function instance(id: string, name: string, methods: string[]) {
  return { id, instance_name: name, identity_link_methods: methods }
}

function identity(instanceId: string, name: string, externalId: string): ApiUserIdentity {
  return {
    instance_id: instanceId,
    instance_name: name,
    external_user_id: externalId,
    link_method: 'admin_set',
    verified_at: null,
    created_at: '2026-01-01T00:00:00Z',
  }
}

// serveAPI registers the three endpoints the fields use. identities is the
// mutable server-side state, so a PUT/DELETE is visible to the next refetch.
function serveAPI(instances: unknown[], identities: ApiUserIdentity[] = [], conflictOn?: string) {
  server.use(
    http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: instances })),
    http.get('/api/v1/users/:id/identities', () => HttpResponse.json({ data: identities })),
    http.put('/api/v1/users/:id/identities/:instanceId', async ({ params, request }) => {
      const body = (await request.json()) as { external_user_id: string }
      if (body.external_user_id === conflictOn) {
        return HttpResponse.json(
          { error: 'external_user_id is already mapped to another user on this instance' },
          { status: 409 },
        )
      }
      const row = identity(params.instanceId as string, 'x', body.external_user_id)
      const at = identities.findIndex((i) => i.instance_id === row.instance_id)
      if (at >= 0) identities[at] = row
      else identities.push(row)
      return HttpResponse.json({ data: row })
    }),
    http.delete('/api/v1/users/:id/identities/:instanceId', ({ params }) => {
      const at = identities.findIndex((i) => i.instance_id === params.instanceId)
      if (at >= 0) identities.splice(at, 1)
      return new HttpResponse(null, { status: 204 })
    }),
  )
}

function renderFields() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <IdentityFields userId="u1" />
    </QueryClientProvider>,
  )
}

describe('IdentityFields', () => {
  it('renders nothing when no instance declares admin_set', async () => {
    serveAPI([
      instance('i1', 'Team chat', ['inbound_code']),
      instance('i2', 'Legacy', []),
    ])
    const { container } = renderFields()

    await waitFor(() => expect(container).toBeEmptyDOMElement())
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  })

  it('renders one field for a single admin_set instance', async () => {
    serveAPI([
      instance('i1', 'Team chat', ['admin_set', 'inbound_code']),
      instance('i2', 'Other', ['inbound_code']),
    ])
    renderFields()

    expect(await screen.findByLabelText('Team chat user ID')).toBeInTheDocument()
    expect(screen.getAllByRole('textbox')).toHaveLength(1)
  })

  it('renders a field per admin_set instance, seeded from the saved identity', async () => {
    serveAPI(
      [instance('i1', 'Team chat', ['admin_set']), instance('i2', 'Ops chat', ['admin_set'])],
      [identity('i2', 'Ops chat', 'U200')],
    )
    renderFields()

    const first = (await screen.findByLabelText('Team chat user ID')) as HTMLInputElement
    const second = screen.getByLabelText('Ops chat user ID') as HTMLInputElement
    expect(first.value).toBe('')
    expect(second.value).toBe('U200')
  })

  it('round-trips set, reload and clear', async () => {
    const user = userEvent.setup()
    serveAPI([instance('i1', 'Team chat', ['admin_set'])])
    const { unmount } = renderFields()

    const input = await screen.findByLabelText('Team chat user ID')
    await user.type(input, 'U123')
    await user.click(screen.getByRole('button', { name: 'Save Team chat user ID' }))
    await waitFor(() => expect(screen.getByLabelText('Team chat user ID')).toHaveValue('U123'))
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Clear Team chat user ID' })).toBeEnabled(),
    )

    // A fresh mount (new query cache) must read the saved value back.
    unmount()
    renderFields()
    expect(await screen.findByDisplayValue('U123')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Clear Team chat user ID' }))
    await waitFor(() => expect(screen.getByLabelText('Team chat user ID')).toHaveValue(''))
    expect(screen.getByRole('button', { name: 'Clear Team chat user ID' })).toBeDisabled()
  })

  it('shows a 409 inline under the field and keeps the typed value', async () => {
    const user = userEvent.setup()
    serveAPI([instance('i1', 'Team chat', ['admin_set'])], [], 'UTAKEN')
    renderFields()

    const input = await screen.findByLabelText('Team chat user ID')
    await user.type(input, 'UTAKEN')
    await user.click(screen.getByRole('button', { name: 'Save Team chat user ID' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Already mapped to another user on this instance.',
    )
    expect(input).toHaveValue('UTAKEN')
    expect(input).toHaveAttribute('aria-invalid', 'true')
  })
})
