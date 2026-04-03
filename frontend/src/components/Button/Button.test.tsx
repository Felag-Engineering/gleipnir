import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { Button } from './Button'

describe('Button', () => {
  it('defaults to type="button"', () => {
    render(<Button>Save</Button>)
    expect(screen.getByRole('button')).toHaveAttribute('type', 'button')
  })

  it.each([
    ['primary', undefined],
    ['secondary', 'secondary'],
    ['ghost', 'ghost'],
    ['danger', 'danger'],
  ] as const)('applies %s variant class', (expected, variant) => {
    render(<Button variant={variant}>Label</Button>)
    expect(screen.getByRole('button').className).toContain(expected)
  })

  it('applies small size class when size="small"', () => {
    render(<Button size="small">Small</Button>)
    expect(screen.getByRole('button').className).toContain('small')
  })

  it('merges a custom className with base classes', () => {
    render(<Button className="custom-class">Merge</Button>)
    const btn = screen.getByRole('button')
    expect(btn.className).toContain('button')
    expect(btn.className).toContain('custom-class')
  })

  it('calls onClick when clicked', () => {
    const onClick = vi.fn()
    render(<Button onClick={onClick}>Click</Button>)
    fireEvent.click(screen.getByRole('button'))
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('does not call onClick when disabled', () => {
    const onClick = vi.fn()
    render(<Button disabled onClick={onClick}>Disabled</Button>)
    fireEvent.click(screen.getByRole('button'))
    expect(onClick).not.toHaveBeenCalled()
  })

  it('forwards the disabled attribute', () => {
    render(<Button disabled>Disabled</Button>)
    expect(screen.getByRole('button')).toBeDisabled()
  })

  it('forwards a ref to the button element', () => {
    const ref = vi.fn()
    render(<Button ref={ref}>Ref</Button>)
    expect(ref).toHaveBeenCalledWith(expect.any(HTMLButtonElement))
  })

  it('passes through arbitrary HTML attributes', () => {
    render(<Button aria-label="custom label" data-testid="btn">Label</Button>)
    const btn = screen.getByTestId('btn')
    expect(btn).toHaveAttribute('aria-label', 'custom label')
  })
})
