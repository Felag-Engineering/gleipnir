import React from 'react'
import { describe, it, expect, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/server'
import userEvent from '@testing-library/user-event'
import SetupPage from './SetupPage'

// useNavigate is mocked so we can assert navigation without a real router.
const mockNavigate = vi.fn()
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>()
  return { ...actual, useNavigate: () => mockNavigate }
})

function renderSetupPage() {
  return render(
    <MemoryRouter>
      <SetupPage />
    </MemoryRouter>,
  )
}

describe('SetupPage', () => {
  it('renders username, password, confirm password fields and a Create account button', () => {
    renderSetupPage()
    expect(screen.getByLabelText(/username/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/^password$/i)).toBeInTheDocument()
    expect(screen.getByLabelText(/confirm password/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /create account/i })).toBeInTheDocument()
  })

  it('navigates to /login on successful setup', async () => {
    server.use(
      http.post('/api/v1/auth/setup', () =>
        HttpResponse.json({ data: { username: 'admin' } }, { status: 201 }),
      ),
    )

    renderSetupPage()

    await userEvent.type(screen.getByLabelText(/username/i), 'admin')
    await userEvent.type(screen.getByLabelText(/^password$/i), 'securepassword')
    await userEvent.type(screen.getByLabelText(/confirm password/i), 'securepassword')
    await userEvent.click(screen.getByRole('button', { name: /create account/i }))

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith('/login')
    })
  })

  it('shows error when passwords do not match', async () => {
    renderSetupPage()

    await userEvent.type(screen.getByLabelText(/username/i), 'admin')
    await userEvent.type(screen.getByLabelText(/^password$/i), 'securepassword')
    await userEvent.type(screen.getByLabelText(/confirm password/i), 'different')
    await userEvent.click(screen.getByRole('button', { name: /create account/i }))

    await waitFor(() => {
      expect(screen.getByText('Passwords do not match')).toBeInTheDocument()
    })
  })

  it('shows error on 403 when setup already completed', async () => {
    server.use(
      http.post('/api/v1/auth/setup', () =>
        HttpResponse.json({ error: 'setup already completed' }, { status: 403 }),
      ),
    )

    renderSetupPage()

    await userEvent.type(screen.getByLabelText(/username/i), 'admin')
    await userEvent.type(screen.getByLabelText(/^password$/i), 'securepassword')
    await userEvent.type(screen.getByLabelText(/confirm password/i), 'securepassword')
    await userEvent.click(screen.getByRole('button', { name: /create account/i }))

    await waitFor(() => {
      expect(screen.getByText('setup already completed')).toBeInTheDocument()
    })
  })
})
