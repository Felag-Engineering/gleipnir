import React from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, waitFor, act } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import { AddInstanceModal } from './AddInstanceModal'
import type { ApiCreatedPluginInstance } from '@/api/types'

function makeClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
}

interface RenderProps {
  pluginId?: string
  pluginName?: string
  existingNames?: string[]
  onClose?: () => void
  onCreated?: (inst: ApiCreatedPluginInstance) => void
}

function renderModal({
  pluginId = 'plugin-slack-01',
  pluginName = 'Slack',
  existingNames = [],
  onClose = vi.fn(),
  onCreated = vi.fn(),
}: RenderProps = {}) {
  return render(
    <QueryClientProvider client={makeClient()}>
      <AddInstanceModal
        pluginId={pluginId}
        pluginName={pluginName}
        existingNames={existingNames}
        onClose={onClose}
        onCreated={onCreated}
      />
    </QueryClientProvider>,
  )
}

function makeInstance(name: string): ApiCreatedPluginInstance {
  return {
    id: 'inst-01',
    plugin_id: 'plugin-slack-01',
    instance_name: name,
    health_state: 'healthy',
    version: 1,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  }
}

// Ensure real timers are restored even if a test fails.
afterEach(() => {
  vi.useRealTimers()
})

describe('AddInstanceModal', () => {
  it('renders title containing the plugin name', () => {
    renderModal()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByText(/Add instance to Slack/i)).toBeInTheDocument()
  })

  it('shows validation error for empty name on submit', async () => {
    renderModal()
    await userEvent.click(screen.getByRole('button', { name: /create instance/i }))

    expect(screen.getByRole('alert')).toHaveTextContent('Instance name is required.')
  })

  it('shows validation error for whitespace-only name on submit', async () => {
    renderModal()
    const input = screen.getByRole('textbox')
    await userEvent.type(input, '   ')
    await userEvent.click(screen.getByRole('button', { name: /create instance/i }))

    expect(screen.getByRole('alert')).toHaveTextContent('Instance name is required.')
  })

  it('shows duplicate-name error and blocks submit when name already exists', async () => {
    let apiCalled = false
    server.use(
      http.post('/api/v1/admin/plugins/:id/instances', () => {
        apiCalled = true
        return HttpResponse.json({ data: makeInstance('production') })
      }),
    )

    renderModal({ existingNames: ['production'] })
    const input = screen.getByRole('textbox')
    await userEvent.type(input, 'production')
    await userEvent.click(screen.getByRole('button', { name: /create instance/i }))

    expect(screen.getByRole('alert')).toHaveTextContent("An instance named 'production' already exists")
    expect(apiCalled).toBe(false)
  })

  it('POSTs {instance_name} to the correct URL and calls onCreated then onClose', async () => {
    let capturedBody: unknown
    server.use(
      http.post('/api/v1/admin/plugins/plugin-slack-01/instances', async ({ request }) => {
        capturedBody = await request.json()
        return HttpResponse.json({ data: makeInstance('production') })
      }),
    )

    const onCreated = vi.fn()
    const onClose = vi.fn()
    renderModal({ onCreated, onClose })

    const input = screen.getByRole('textbox')
    await userEvent.type(input, 'production')
    await userEvent.click(screen.getByRole('button', { name: /create instance/i }))

    await waitFor(() => expect(onClose).toHaveBeenCalledOnce())
    expect(capturedBody).toEqual({ instance_name: 'production' })
    expect(onCreated).toHaveBeenCalledOnce()
    // onCreated must be called before onClose
    const createdOrder = onCreated.mock.invocationCallOrder[0]
    const closedOrder = onClose.mock.invocationCallOrder[0]
    expect(createdOrder).toBeLessThan(closedOrder)
  })

  it('shows 400 error with "Failed to create instance:" prefix and server message', async () => {
    server.use(
      http.post('/api/v1/admin/plugins/:id/instances', () =>
        HttpResponse.json(
          { error: 'instance_name must not be empty', detail: 'trimmed to empty' },
          { status: 400 },
        ),
      ),
    )

    renderModal()
    const input = screen.getByRole('textbox')
    await userEvent.type(input, 'x')
    await userEvent.click(screen.getByRole('button', { name: /create instance/i }))

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('Failed to create instance:')
    expect(screen.getByRole('alert')).toHaveTextContent('instance_name must not be empty')
  })

  it('shows 404 message and calls onClose after delay', async () => {
    server.use(
      http.post('/api/v1/admin/plugins/:id/instances', () =>
        HttpResponse.json({ error: 'plugin not found' }, { status: 404 }),
      ),
    )

    vi.useFakeTimers({ shouldAdvanceTime: true })
    const onClose = vi.fn()
    renderModal({ onClose })

    const input = screen.getByRole('textbox')
    await userEvent.type(input, 'production')
    await userEvent.click(screen.getByRole('button', { name: /create instance/i }))

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('plugin no longer exists')

    // Advance the 1500ms delay and check modal closes.
    await act(async () => {
      vi.advanceTimersByTime(1500)
    })
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('shows 409 error with server message verbatim', async () => {
    server.use(
      http.post('/api/v1/admin/plugins/:id/instances', () =>
        HttpResponse.json(
          { error: "instance named 'production' already exists for this plugin" },
          { status: 409 },
        ),
      ),
    )

    renderModal()
    const input = screen.getByRole('textbox')
    await userEvent.type(input, 'production')
    await userEvent.click(screen.getByRole('button', { name: /create instance/i }))

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('Failed to create instance:')
    expect(screen.getByRole('alert')).toHaveTextContent("instance named 'production' already exists")
  })

  it('shows 500 error with server message', async () => {
    server.use(
      http.post('/api/v1/admin/plugins/:id/instances', () =>
        HttpResponse.json({ error: 'internal server error' }, { status: 500 }),
      ),
    )

    renderModal()
    const input = screen.getByRole('textbox')
    await userEvent.type(input, 'any-name')
    await userEvent.click(screen.getByRole('button', { name: /create instance/i }))

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('Failed to create instance:')
    expect(screen.getByRole('alert')).toHaveTextContent('internal server error')
  })

  it('calls onClose when Cancel is clicked', async () => {
    const onClose = vi.fn()
    renderModal({ onClose })

    await userEvent.click(screen.getByRole('button', { name: /cancel/i }))

    expect(onClose).toHaveBeenCalledOnce()
  })

  it('calls onClose on Escape key', async () => {
    const onClose = vi.fn()
    renderModal({ onClose })

    await userEvent.keyboard('{Escape}')

    expect(onClose).toHaveBeenCalledOnce()
  })

  it('calls onClose when overlay is clicked', async () => {
    const onClose = vi.fn()
    renderModal({ onClose })

    const overlay = screen.getByTestId('modal-overlay')
    await userEvent.click(overlay)

    expect(onClose).toHaveBeenCalledOnce()
  })
})
