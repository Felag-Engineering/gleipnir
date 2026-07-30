import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import { InstallPluginButton } from './InstallPluginButton'

function makeClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
}

function renderButton(props: React.ComponentProps<typeof InstallPluginButton> = {}) {
  return render(
    <QueryClientProvider client={makeClient()}>
      <MemoryRouter>
        <InstallPluginButton {...props} />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function makeFile(sizeBytes: number, name = 'plugin.tar.gz'): File {
  const content = new Uint8Array(sizeBytes)
  return new File([content], name, { type: 'application/gzip' })
}

const PLUGIN_RESPONSE = {
  id: 'plugin-slack-01',
  name: 'Slack',
  version: '1.2.0',
  status: 'active',
}

describe('InstallPluginButton', () => {
  it('renders the install button in idle state', () => {
    renderButton()
    expect(screen.getByRole('button', { name: /install plugin/i })).toBeInTheDocument()
  })

  it('triggers hidden file input on button click', async () => {
    renderButton()
    const button = screen.getByRole('button', { name: /install plugin/i })
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const clickSpy = vi.spyOn(input, 'click')

    await userEvent.click(button)

    expect(clickSpy).toHaveBeenCalledOnce()
  })

  it('shows size error and does NOT call the API when file exceeds 100 MiB', async () => {
    let apiCalled = false
    server.use(
      http.post('/api/v1/admin/plugins', () => {
        apiCalled = true
        return HttpResponse.json({ data: PLUGIN_RESPONSE })
      }),
    )

    renderButton()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement

    const oversizedFile = makeFile(101 * 1024 * 1024)
    await userEvent.upload(input, oversizedFile)

    expect(screen.getByRole('alert')).toHaveTextContent(/tarball too large/i)
    expect(apiCalled).toBe(false)
  })

  it('POSTs file with Content-Type: application/octet-stream', async () => {
    let capturedContentType: string | null = null
    server.use(
      http.post('/api/v1/admin/plugins', ({ request }) => {
        capturedContentType = request.headers.get('Content-Type')
        return HttpResponse.json({ data: PLUGIN_RESPONSE })
      }),
    )

    renderButton()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const validFile = makeFile(1024)

    await userEvent.upload(input, validFile)

    await waitFor(() =>
      expect(screen.getByRole('status')).toBeInTheDocument(),
    )
    expect(capturedContentType).toBe('application/octet-stream')
  })

  it('shows persistent success card after install and calls onInstalled', async () => {
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json({ data: PLUGIN_RESPONSE }),
      ),
    )

    const onInstalled = vi.fn()
    renderButton({ onInstalled })
    const input = document.querySelector('input[type="file"]') as HTMLInputElement

    await userEvent.upload(input, makeFile(1024))

    await waitFor(() =>
      expect(screen.getByRole('status')).toBeInTheDocument(),
    )
    expect(screen.getByText(/Installed/)).toBeInTheDocument()
    expect(screen.getByText(/Slack/)).toBeInTheDocument()
    expect(onInstalled).toHaveBeenCalledWith(PLUGIN_RESPONSE)
  })

  it('success card persists — no timer-based auto-clear', async () => {
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json({ data: PLUGIN_RESPONSE }),
      ),
    )

    renderButton({ hasInstancesForPlugin: () => false })
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    await userEvent.upload(input, makeFile(1024))

    await waitFor(() => expect(screen.getByRole('status')).toBeInTheDocument())

    // Wait a bit to confirm card does not disappear automatically.
    await new Promise((r) => setTimeout(r, 200))
    expect(screen.getByRole('status')).toBeInTheDocument()
  })

  it('clicking Dismiss removes the success card', async () => {
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json({ data: PLUGIN_RESPONSE }),
      ),
    )

    renderButton()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    await userEvent.upload(input, makeFile(1024))

    await waitFor(() => expect(screen.getByRole('status')).toBeInTheDocument())

    await userEvent.click(screen.getByRole('button', { name: /dismiss/i }))

    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('auto-clears success card when hasInstancesForPlugin returns true', async () => {
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json({ data: PLUGIN_RESPONSE }),
      ),
    )

    // Starts false, then the parent callback flips to true.
    let hasInstances = false
    const { rerender } = renderButton({
      hasInstancesForPlugin: () => hasInstances,
    })

    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    await userEvent.upload(input, makeFile(1024))

    await waitFor(() => expect(screen.getByRole('status')).toBeInTheDocument())

    // Simulate the parent telling us this plugin now has instances.
    hasInstances = true
    rerender(
      <QueryClientProvider client={makeClient()}>
        <MemoryRouter>
          <InstallPluginButton hasInstancesForPlugin={() => hasInstances} />
        </MemoryRouter>
      </QueryClientProvider>,
    )

    await waitFor(() => expect(screen.queryByRole('status')).not.toBeInTheDocument())
  })

  it('shows 400 error with server message prefixed by "Install failed:"', async () => {
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json(
          { error: 'Invalid tarball', detail: 'manifest missing name field' },
          { status: 400 },
        ),
      ),
    )

    renderButton()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    await userEvent.upload(input, makeFile(1024))

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('Install failed: Invalid tarball')
  })

  it('shows 409 error with verbatim server message', async () => {
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json(
          { error: 'concurrent plugin update; retry' },
          { status: 409 },
        ),
      ),
    )

    renderButton()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    await userEvent.upload(input, makeFile(1024))

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    // The server message "concurrent plugin update; retry" must appear verbatim.
    expect(screen.getByRole('alert')).toHaveTextContent('concurrent plugin update; retry')
    expect(screen.getByRole('alert')).toHaveTextContent('Install failed:')
  })

  it('shows fixed 413 message regardless of server body', async () => {
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        new HttpResponse(null, { status: 413 }),
      ),
    )

    renderButton()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    await userEvent.upload(input, makeFile(1024))

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('Tarball too large (max 100 MiB).')
  })

  it('shows 422 error with server message', async () => {
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json(
          { error: 'Signature verification failed', detail: 'pubkey mismatch' },
          { status: 422 },
        ),
      ),
    )

    renderButton()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    await userEvent.upload(input, makeFile(1024))

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('Install failed: Signature verification failed')
  })

  it('shows "Review & approve" link (not "Add instance") when status is pending_review', async () => {
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json({
          data: { id: 'plugin-new-01', name: 'NewPlugin', version: '0.1.0', status: 'pending_review' },
        }),
      ),
    )

    renderButton()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    await userEvent.upload(input, makeFile(1024))

    await waitFor(() => expect(screen.getByRole('status')).toBeInTheDocument())

    // "Review & approve" link must be present; "Add instance" must NOT be present.
    expect(screen.getByRole('link', { name: /review.*approve/i })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /add instance/i })).not.toBeInTheDocument()
  })

  it('shows "Add instance" button (not review link) when status is active', async () => {
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json({ data: PLUGIN_RESPONSE }), // status: 'active'
      ),
    )

    renderButton()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    await userEvent.upload(input, makeFile(1024))

    await waitFor(() => expect(screen.getByRole('status')).toBeInTheDocument())

    // "Add instance" button must be present; review link must NOT be present.
    expect(screen.getByRole('button', { name: /add instance/i })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /review.*approve/i })).not.toBeInTheDocument()
  })

  it('shows dedicated amber notice for 503 (plugins disabled)', async () => {
    server.use(
      http.post('/api/v1/admin/plugins', () =>
        HttpResponse.json({ error: 'plugin system disabled' }, { status: 503 }),
      ),
    )

    renderButton()
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    await userEvent.upload(input, makeFile(1024))

    await waitFor(() => expect(screen.getByRole('alert')).toBeInTheDocument())
    expect(screen.getByRole('alert')).toHaveTextContent('GLEIPNIR_PLUGINS_ENABLED=true')
  })
})
