import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import userEvent from '@testing-library/user-event'
import LoginPage from './LoginPage'

// useNavigate is mocked so we can assert navigation without a real router.
const mockNavigate = vi.fn()
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>()
  return { ...actual, useNavigate: () => mockNavigate }
})

function renderLoginPage() {
  return render(
    <MemoryRouter>
      <LoginPage />
    </MemoryRouter>,
  )
}

describe('LoginPage', () => {
  it('renders username and password fields and a Sign in button', () => {
    renderLoginPage()
    expect(screen.getByLabelText(/username/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/password/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /sign in/i })).toBeInTheDocument()
  })

  it('navigates to /dashboard on successful login', async () => {
    server.use(
      http.post('/api/v1/auth/login', () =>
        HttpResponse.json({ data: { username: 'alice' } }),
      ),
    )

    renderLoginPage()

    await userEvent.type(screen.getByLabelText(/username/i), 'alice')
    await userEvent.type(screen.getByLabelText(/password/i), 'correct-password')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith('/dashboard')
    })
  })

  it('shows error message on 401 invalid credentials', async () => {
    server.use(
      http.post('/api/v1/auth/login', () =>
        HttpResponse.json({ error: 'invalid credentials' }, { status: 401 }),
      ),
    )

    renderLoginPage()

    await userEvent.type(screen.getByLabelText(/username/i), 'alice')
    await userEvent.type(screen.getByLabelText(/password/i), 'wrong-password')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))

    await waitFor(() => {
      expect(screen.getByText('invalid credentials')).toBeInTheDocument()
    })
  })

  it('disables button while loading', async () => {
    // Use a promise that never resolves to keep the loading state.
    server.use(
      http.post('/api/v1/auth/login', () => new Promise(() => {})),
    )

    renderLoginPage()

    await userEvent.type(screen.getByLabelText(/username/i), 'alice')
    await userEvent.type(screen.getByLabelText(/password/i), 'pass')

    const btn = screen.getByRole('button', { name: /sign in/i })
    await userEvent.click(btn)

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /signing in/i })).toBeDisabled()
    })
  })

  it('shows session expired banner when ?expired=1 is in the URL', () => {
    render(
      <MemoryRouter initialEntries={['/login?expired=1']}>
        <LoginPage />
      </MemoryRouter>,
    )
    expect(screen.getByText('Session expired. Please sign in again.')).toBeInTheDocument()
  })

  it('hides session expired banner once an error is shown', async () => {
    server.use(
      http.post('/api/v1/auth/login', () =>
        HttpResponse.json({ error: 'invalid credentials' }, { status: 401 }),
      ),
    )

    render(
      <MemoryRouter initialEntries={['/login?expired=1']}>
        <LoginPage />
      </MemoryRouter>,
    )
    expect(screen.getByText('Session expired. Please sign in again.')).toBeInTheDocument()

    await userEvent.type(screen.getByLabelText(/username/i), 'alice')
    await userEvent.type(screen.getByLabelText(/password/i), 'wrong')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))

    await waitFor(() => {
      expect(screen.getByText('invalid credentials')).toBeInTheDocument()
    })
    expect(screen.queryByText('Session expired. Please sign in again.')).not.toBeInTheDocument()
  })

  it('disables input fields while loading', async () => {
    server.use(
      http.post('/api/v1/auth/login', () => new Promise(() => {})),
    )

    renderLoginPage()

    await userEvent.type(screen.getByLabelText(/username/i), 'alice')
    await userEvent.type(screen.getByLabelText(/password/i), 'pass')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))

    await waitFor(() => {
      expect(screen.getByLabelText(/username/i)).toBeDisabled()
      expect(screen.getByLabelText(/password/i)).toBeDisabled()
    })
  })
})
