import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { AddServerModal } from './AddServerModal'

const testMutate = vi.fn()

// Mock useTestMcpConnection so tests don't need a QueryClientProvider.
vi.mock('@/hooks/mutations/servers', () => ({
  useTestMcpConnection: () => ({
    mutate: testMutate,
    isPending: false,
    isError: false,
    data: undefined,
    reset: vi.fn(),
  }),
}))

const noop = vi.fn()

const FAKE_CERT_PEM = '-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----'

describe('AddServerModal', () => {
  it('renders name and URL fields', () => {
    render(
      <AddServerModal
        onClose={noop}
        onSubmit={noop}
        isPending={false}
        error={null}
      />,
    )
    expect(screen.getByLabelText(/name/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/url/i)).toBeInTheDocument()
  })

  it('submit calls onSubmit with name, url, and headers', () => {
    const onSubmit = vi.fn()
    render(
      <AddServerModal
        onClose={noop}
        onSubmit={onSubmit}
        isPending={false}
        error={null}
      />,
    )

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'my-server' } })
    fireEvent.change(screen.getByLabelText(/url/i), { target: { value: 'http://localhost:8080' } })

    // Add a header row.
    fireEvent.click(screen.getByRole('button', { name: /add header/i }))
    const keyInputs = screen.getAllByRole('textbox', { name: /header name 1/i })
    const valueInputs = screen.getAllByRole('textbox', { name: /header value 1/i })
    fireEvent.change(keyInputs[0], { target: { value: 'x-api-key' } })
    fireEvent.change(valueInputs[0], { target: { value: 'sk-secret' } })

    fireEvent.submit(document.getElementById('add-server-form')!)
    expect(onSubmit).toHaveBeenCalledWith('my-server', 'http://localhost:8080', [
      { key: 'x-api-key', value: 'sk-secret' },
    ], '', null, null)
  })

  it('submit without headers passes empty array', () => {
    const onSubmit = vi.fn()
    render(
      <AddServerModal
        onClose={noop}
        onSubmit={onSubmit}
        isPending={false}
        error={null}
      />,
    )

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'my-server' } })
    fireEvent.change(screen.getByLabelText(/url/i), { target: { value: 'http://localhost:8080' } })

    fireEvent.submit(document.getElementById('add-server-form')!)
    expect(onSubmit).toHaveBeenCalledWith('my-server', 'http://localhost:8080', [], '', null, null)
  })

  it('passes the CA certificate textarea value to onSubmit', () => {
    const onSubmit = vi.fn()
    render(
      <AddServerModal
        onClose={noop}
        onSubmit={onSubmit}
        isPending={false}
        error={null}
      />,
    )

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'my-server' } })
    fireEvent.change(screen.getByLabelText(/url/i), { target: { value: 'https://localhost:8443' } })
    fireEvent.change(screen.getByLabelText(/ca certificate/i), { target: { value: FAKE_CERT_PEM } })

    fireEvent.submit(document.getElementById('add-server-form')!)
    expect(onSubmit).toHaveBeenCalledWith('my-server', 'https://localhost:8443', [], FAKE_CERT_PEM, null, null)
  })

  it('submits the parsed call timeout when set', () => {
    const onSubmit = vi.fn()
    render(
      <AddServerModal
        onClose={noop}
        onSubmit={onSubmit}
        isPending={false}
        error={null}
      />,
    )

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'my-server' } })
    fireEvent.change(screen.getByLabelText(/url/i), { target: { value: 'http://localhost:8080' } })
    fireEvent.change(screen.getByLabelText(/call timeout/i), { target: { value: '120' } })

    fireEvent.submit(document.getElementById('add-server-form')!)
    expect(onSubmit).toHaveBeenCalledWith('my-server', 'http://localhost:8080', [], '', 120, null)
  })

  it('shows an inline error and blocks submit for an out-of-range call timeout', () => {
    const onSubmit = vi.fn()
    render(
      <AddServerModal
        onClose={noop}
        onSubmit={onSubmit}
        isPending={false}
        error={null}
      />,
    )

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'my-server' } })
    fireEvent.change(screen.getByLabelText(/url/i), { target: { value: 'http://localhost:8080' } })
    fireEvent.change(screen.getByLabelText(/call timeout/i), { target: { value: '700' } })

    expect(screen.getByText(/must be between 1 and 600 seconds/i)).toBeInTheDocument()

    fireEvent.submit(document.getElementById('add-server-form')!)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('includes the CA certificate in the test-connection request', () => {
    testMutate.mockClear()
    render(
      <AddServerModal
        onClose={noop}
        onSubmit={noop}
        isPending={false}
        error={null}
      />,
    )

    fireEvent.change(screen.getByLabelText(/url/i), { target: { value: 'https://localhost:8443' } })
    fireEvent.change(screen.getByLabelText(/ca certificate/i), { target: { value: FAKE_CERT_PEM } })
    fireEvent.change(screen.getByLabelText(/call timeout/i), { target: { value: '120' } })
    fireEvent.click(screen.getByRole('button', { name: /test connection/i }))

    // call_timeout_seconds is never honored by TestConnection (see
    // mcp_handler.go's doc); the payload must not carry it even when the
    // field is filled in.
    expect(testMutate).toHaveBeenCalledWith({
      url: 'https://localhost:8443',
      auth_headers: undefined,
      ca_cert_pem: FAKE_CERT_PEM,
    })
  })

  it('can add and remove header rows', () => {
    render(
      <AddServerModal
        onClose={noop}
        onSubmit={noop}
        isPending={false}
        error={null}
      />,
    )

    // No rows initially.
    expect(screen.queryByLabelText(/header name 1/i)).not.toBeInTheDocument()

    // Add one row.
    fireEvent.click(screen.getByRole('button', { name: /add header/i }))
    expect(screen.getByLabelText(/header name 1/i)).toBeInTheDocument()

    // Remove it.
    fireEvent.click(screen.getByRole('button', { name: /remove header 1/i }))
    expect(screen.queryByLabelText(/header name 1/i)).not.toBeInTheDocument()
  })

  it('run attribution defaults to Off and calls onSubmit with null', () => {
    const onSubmit = vi.fn()
    render(
      <AddServerModal
        onClose={noop}
        onSubmit={onSubmit}
        isPending={false}
        error={null}
      />,
    )

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'my-server' } })
    fireEvent.change(screen.getByLabelText(/url/i), { target: { value: 'http://localhost:8080' } })

    fireEvent.submit(document.getElementById('add-server-form')!)
    const [, , , , , runAttribution] = onSubmit.mock.calls[0]
    expect(runAttribution).toBeNull()
  })

  it('selecting Relay gives {mode: "relay"}', () => {
    const onSubmit = vi.fn()
    render(
      <AddServerModal
        onClose={noop}
        onSubmit={onSubmit}
        isPending={false}
        error={null}
      />,
    )

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'my-server' } })
    fireEvent.change(screen.getByLabelText(/url/i), { target: { value: 'http://localhost:8080' } })
    fireEvent.change(screen.getByLabelText(/run attribution/i), { target: { value: 'relay' } })

    fireEvent.submit(document.getElementById('add-server-form')!)
    const [, , , , , runAttribution] = onSubmit.mock.calls[0]
    expect(runAttribution).toEqual({ mode: 'relay' })
  })

  it('Custom with an invalid name disables submit', () => {
    const onSubmit = vi.fn()
    render(
      <AddServerModal
        onClose={noop}
        onSubmit={onSubmit}
        isPending={false}
        error={null}
      />,
    )

    fireEvent.change(screen.getByLabelText(/name/i), { target: { value: 'my-server' } })
    fireEvent.change(screen.getByLabelText(/url/i), { target: { value: 'http://localhost:8080' } })
    fireEvent.change(screen.getByLabelText(/run attribution/i), { target: { value: 'custom' } })
    fireEvent.change(screen.getByLabelText(/on-behalf-of header name/i), { target: { value: 'X_Actor' } })

    expect(screen.getByRole('button', { name: /add mcp server/i })).toBeDisabled()

    fireEvent.submit(document.getElementById('add-server-form')!)
    expect(onSubmit).not.toHaveBeenCalled()
  })

  it('calls onClose when Cancel is clicked', () => {
    const onClose = vi.fn()
    render(
      <AddServerModal
        onClose={onClose}
        onSubmit={noop}
        isPending={false}
        error={null}
      />,
    )
    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    expect(onClose).toHaveBeenCalledOnce()
  })
})
