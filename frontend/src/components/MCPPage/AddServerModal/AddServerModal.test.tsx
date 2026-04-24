import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { AddServerModal } from './AddServerModal'

// Mock useTestMcpConnection so tests don't need a QueryClientProvider.
vi.mock('@/hooks/mutations/servers', () => ({
  useTestMcpConnection: () => ({
    mutate: vi.fn(),
    isPending: false,
    isError: false,
    data: undefined,
    reset: vi.fn(),
  }),
}))

const noop = vi.fn()

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
    ])
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
    expect(onSubmit).toHaveBeenCalledWith('my-server', 'http://localhost:8080', [])
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
