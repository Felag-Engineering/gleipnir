import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { RouterProvider } from 'react-router/dom'
import QueryProvider from '@/api/QueryProvider'

describe('lazy routes', () => {
  it('loads a page chunk on demand and renders it', async () => {
    window.history.pushState({}, '', '/login')
    const { default: router } = await import('./routes')
    render(
      <QueryProvider>
        <RouterProvider router={router} />
      </QueryProvider>,
    )
    expect(await screen.findByLabelText(/username/i)).toBeInTheDocument()
  })
})
