import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { ModalFooter } from './ModalFooter'

describe('ModalFooter', () => {
  it('renders cancel and submit buttons with labels', () => {
    render(
      <ModalFooter onCancel={vi.fn()} isLoading={false} submitLabel="Save" />,
    )
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument()
  })

  it('calls onCancel when Cancel is clicked', () => {
    const onCancel = vi.fn()
    render(<ModalFooter onCancel={onCancel} isLoading={false} submitLabel="Save" />)
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalledTimes(1)
  })

  it('calls onSubmit when submit button is clicked (non-form mode)', () => {
    const onSubmit = vi.fn()
    render(
      <ModalFooter onCancel={vi.fn()} onSubmit={onSubmit} isLoading={false} submitLabel="Delete" />,
    )
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(onSubmit).toHaveBeenCalledTimes(1)
  })

  it('uses form attribute when formId is provided', () => {
    render(
      <ModalFooter onCancel={vi.fn()} formId="my-form" isLoading={false} submitLabel="Submit" />,
    )
    const submitBtn = screen.getByRole('button', { name: 'Submit' })
    expect(submitBtn).toHaveAttribute('type', 'submit')
    expect(submitBtn).toHaveAttribute('form', 'my-form')
  })

  it('disables both buttons when isLoading is true', () => {
    render(
      <ModalFooter onCancel={vi.fn()} isLoading={true} submitLabel="Save" />,
    )
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled()
    // submit button shows loading label — find by accessible name
    const buttons = screen.getAllByRole('button')
    buttons.forEach((btn) => expect(btn).toBeDisabled())
  })

  it('shows loadingLabel and spinner when isLoading', () => {
    render(
      <ModalFooter
        onCancel={vi.fn()}
        isLoading={true}
        submitLabel="Save"
        loadingLabel="Saving…"
      />,
    )
    expect(screen.getByText('Saving…')).toBeInTheDocument()
  })

  it('defaults loadingLabel to submitLabel when loadingLabel omitted', () => {
    render(
      <ModalFooter onCancel={vi.fn()} isLoading={true} submitLabel="Save" />,
    )
    expect(screen.getByText('Save')).toBeInTheDocument()
  })

  it('disables submit when submitDisabled is true', () => {
    render(
      <ModalFooter
        onCancel={vi.fn()}
        isLoading={false}
        submitLabel="Save"
        submitDisabled={true}
      />,
    )
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('applies danger variant to submit button and uses danger spinner class when loading', () => {
    render(
      <ModalFooter
        onCancel={vi.fn()}
        isLoading={true}
        submitLabel="Delete"
        loadingLabel="Deleting…"
        variant="danger"
      />,
    )
    const spinner = document.querySelector('[aria-hidden="true"]')
    expect(spinner).not.toBeNull()
    expect(spinner!.className).toContain('spinnerDanger')
  })

  it('applies primary variant spinner class when loading', () => {
    render(
      <ModalFooter
        onCancel={vi.fn()}
        isLoading={true}
        submitLabel="Save"
        loadingLabel="Saving…"
        variant="primary"
      />,
    )
    const spinner = document.querySelector('[aria-hidden="true"]')
    expect(spinner).not.toBeNull()
    // primary uses .spinner (not .spinnerDanger)
    expect(spinner!.className).not.toContain('spinnerDanger')
    expect(spinner!.className).toContain('spinner')
  })
})
