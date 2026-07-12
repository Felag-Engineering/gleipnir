import { describe, it, expect, vi } from 'vitest'
import { useState } from 'react'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { UserMenu } from './UserMenu'

vi.mock('@/api/auth', () => ({
  logout: vi.fn().mockResolvedValue(undefined),
}))

// Harness that owns the open state and renders a real trigger button, so the
// focus-restoration behaviour (capture activeElement on open, restore on close)
// can be exercised end to end.
function Harness({ initialOpen = false }: { initialOpen?: boolean }) {
  const [open, setOpen] = useState(initialOpen)
  return (
    <MemoryRouter>
      <button data-testid="trigger" onClick={() => setOpen(true)}>
        trigger
      </button>
      <UserMenu open={open} onClose={() => setOpen(false)} />
    </MemoryRouter>
  )
}

function openFromTrigger() {
  render(<Harness />)
  const trigger = screen.getByTestId('trigger')
  trigger.focus()
  fireEvent.click(trigger)
  return trigger
}

describe('UserMenu — rendering', () => {
  it('does not render menu content when closed', () => {
    render(<Harness />)
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('renders both menu items when open', () => {
    openFromTrigger()
    const items = screen.getAllByRole('menuitem')
    expect(items).toHaveLength(2)
    expect(items[0]).toHaveTextContent('Settings')
    expect(items[1]).toHaveTextContent('Log out')
  })

  it('menu has role="menu" and vertical orientation', () => {
    openFromTrigger()
    const menu = screen.getByRole('menu')
    expect(menu).toHaveAttribute('aria-orientation', 'vertical')
  })
})

describe('UserMenu — focus management', () => {
  it('moves focus to the first item on open', () => {
    openFromTrigger()
    const items = screen.getAllByRole('menuitem')
    expect(document.activeElement).toBe(items[0])
  })

  it('gives the first item the roving tabindex on open', () => {
    openFromTrigger()
    const items = screen.getAllByRole('menuitem')
    expect(items[0]).toHaveAttribute('tabindex', '0')
    expect(items[1]).toHaveAttribute('tabindex', '-1')
  })

  it('restores focus to the trigger when Escape closes the menu', () => {
    const trigger = openFromTrigger()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    expect(document.activeElement).toBe(trigger)
  })
})

describe('UserMenu — roving arrow navigation', () => {
  it('ArrowDown moves focus to the next item and shifts the roving tabindex', () => {
    openFromTrigger()
    const menu = screen.getByRole('menu')
    const items = screen.getAllByRole('menuitem')
    fireEvent.keyDown(menu, { key: 'ArrowDown' })
    expect(document.activeElement).toBe(items[1])
    expect(items[1]).toHaveAttribute('tabindex', '0')
    expect(items[0]).toHaveAttribute('tabindex', '-1')
  })

  it('ArrowUp from the first item wraps to the last item', () => {
    openFromTrigger()
    const menu = screen.getByRole('menu')
    const items = screen.getAllByRole('menuitem')
    fireEvent.keyDown(menu, { key: 'ArrowUp' })
    expect(document.activeElement).toBe(items[1])
  })

  it('ArrowDown from the last item wraps to the first item', () => {
    openFromTrigger()
    const menu = screen.getByRole('menu')
    const items = screen.getAllByRole('menuitem')
    fireEvent.keyDown(menu, { key: 'ArrowDown' })
    fireEvent.keyDown(menu, { key: 'ArrowDown' })
    expect(document.activeElement).toBe(items[0])
  })

  it('Home focuses the first item and End focuses the last item', () => {
    openFromTrigger()
    const menu = screen.getByRole('menu')
    const items = screen.getAllByRole('menuitem')
    fireEvent.keyDown(menu, { key: 'End' })
    expect(document.activeElement).toBe(items[1])
    fireEvent.keyDown(menu, { key: 'Home' })
    expect(document.activeElement).toBe(items[0])
  })
})

describe('UserMenu — close interactions', () => {
  it('closes on click outside the menu', () => {
    openFromTrigger()
    expect(screen.getByRole('menu')).toBeInTheDocument()
    fireEvent.mouseDown(document.body)
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
  })

  it('does not close on a click inside the menu', () => {
    openFromTrigger()
    const menu = screen.getByRole('menu')
    fireEvent.mouseDown(menu)
    expect(screen.getByRole('menu')).toBeInTheDocument()
  })
})
