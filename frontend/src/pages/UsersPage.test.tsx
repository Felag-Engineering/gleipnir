import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

import UsersPage from './UsersPage'
import type { ApiUser } from '@/api/types'

// --- Mocks ---

vi.mock('@/hooks/useUsers')
vi.mock('@/hooks/useCreateUser')
vi.mock('@/hooks/useUpdateUser')

import { useUsers } from '@/hooks/useUsers'
import { useCreateUser } from '@/hooks/useCreateUser'
import { useUpdateUser } from '@/hooks/useUpdateUser'

// --- Fixtures ---

const USER_ADMIN: ApiUser = {
  id: 'u1',
  username: 'alice',
  roles: ['admin'],
  created_at: '2026-01-01T00:00:00Z',
  deactivated_at: null,
}

const USER_OPERATOR: ApiUser = {
  id: 'u2',
  username: 'bob',
  roles: ['operator'],
  created_at: '2026-01-02T00:00:00Z',
  deactivated_at: null,
}

const USER_DEACTIVATED: ApiUser = {
  id: 'u3',
  username: 'charlie',
  roles: [],
  created_at: '2026-01-03T00:00:00Z',
  deactivated_at: '2026-02-01T00:00:00Z',
}

// --- Helpers ---

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

function renderPage(queryClient = makeQueryClient()) {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <UsersPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

function mockNoopMutations() {
  const noop = { mutate: vi.fn(), isPending: false, error: null, reset: vi.fn() }
  vi.mocked(useCreateUser).mockReturnValue(noop as unknown as ReturnType<typeof useCreateUser>)
  vi.mocked(useUpdateUser).mockReturnValue(noop as unknown as ReturnType<typeof useUpdateUser>)
}

function mockUsersLoaded(users: ApiUser[]) {
  vi.mocked(useUsers).mockReturnValue({
    data: users,
    status: 'success',
  } as ReturnType<typeof useUsers>)
}

function mockUsersPending() {
  vi.mocked(useUsers).mockReturnValue({
    data: undefined,
    status: 'pending',
  } as ReturnType<typeof useUsers>)
}

function mockUsersError() {
  vi.mocked(useUsers).mockReturnValue({
    data: undefined,
    status: 'error',
  } as ReturnType<typeof useUsers>)
}

// --- Tests ---

describe('UsersPage — loading state', () => {
  beforeEach(() => {
    mockUsersPending()
    mockNoopMutations()
  })

  it('shows skeleton while loading', () => {
    renderPage()
    const skeletons = document.querySelectorAll('[aria-hidden="true"]')
    expect(skeletons.length).toBeGreaterThan(0)
  })

  it('does not show user table while loading', () => {
    renderPage()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })
})

describe('UsersPage — error state', () => {
  beforeEach(() => {
    mockUsersError()
    mockNoopMutations()
  })

  it('shows error message on failure', () => {
    renderPage()
    expect(screen.getByText(/failed to load users/i)).toBeInTheDocument()
  })
})

describe('UsersPage — empty state', () => {
  beforeEach(() => {
    mockUsersLoaded([])
    mockNoopMutations()
  })

  it('shows empty state when no users', () => {
    renderPage()
    expect(screen.getByText(/no users/i)).toBeInTheDocument()
  })
})

describe('UsersPage — users loaded', () => {
  beforeEach(() => {
    mockUsersLoaded([USER_ADMIN, USER_OPERATOR, USER_DEACTIVATED])
    mockNoopMutations()
  })

  it('renders all usernames', () => {
    renderPage()
    expect(screen.getByText('alice')).toBeInTheDocument()
    expect(screen.getByText('bob')).toBeInTheDocument()
    expect(screen.getByText('charlie')).toBeInTheDocument()
  })

  it('shows Active badge for active users', () => {
    renderPage()
    const activeBadges = screen.getAllByText('Active')
    expect(activeBadges.length).toBe(2)
  })

  it('shows Deactivated badge for deactivated users', () => {
    renderPage()
    expect(screen.getByText('Deactivated')).toBeInTheDocument()
  })

  it('shows Reactivate button for deactivated users', () => {
    renderPage()
    expect(screen.getByRole('button', { name: /reactivate/i })).toBeInTheDocument()
  })

  it('shows Deactivate button for active users', () => {
    renderPage()
    const deactivateBtns = screen.getAllByRole('button', { name: /deactivate/i })
    // Two active users → two deactivate buttons (plus the header Create button excluded)
    expect(deactivateBtns.length).toBe(2)
  })
})

describe('UsersPage — role chip interaction', () => {
  it('calls updateMutation.mutate when a role chip is clicked', () => {
    mockUsersLoaded([USER_OPERATOR])
    const mutateMock = vi.fn()
    vi.mocked(useUpdateUser).mockReturnValue({
      mutate: mutateMock,
      isPending: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useUpdateUser>)
    vi.mocked(useCreateUser).mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useCreateUser>)

    renderPage()

    // Click "admin" chip to add admin role to bob (who currently has operator)
    fireEvent.click(screen.getByTitle('Add admin role'))

    expect(mutateMock).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'u2', roles: expect.arrayContaining(['admin', 'operator']) }),
    )
  })
})

describe('UsersPage — deactivate action', () => {
  it('calls updateMutation.mutate with deactivated=true', () => {
    mockUsersLoaded([USER_ADMIN])
    const mutateMock = vi.fn()
    vi.mocked(useUpdateUser).mockReturnValue({
      mutate: mutateMock,
      isPending: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useUpdateUser>)
    vi.mocked(useCreateUser).mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useCreateUser>)

    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /deactivate/i }))

    expect(mutateMock).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'u1', deactivated: true }),
    )
  })

  it('calls updateMutation.mutate with deactivated=false for reactivation', () => {
    mockUsersLoaded([USER_DEACTIVATED])
    const mutateMock = vi.fn()
    vi.mocked(useUpdateUser).mockReturnValue({
      mutate: mutateMock,
      isPending: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useUpdateUser>)
    vi.mocked(useCreateUser).mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useCreateUser>)

    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /reactivate/i }))

    expect(mutateMock).toHaveBeenCalledWith(
      expect.objectContaining({ id: 'u3', deactivated: false }),
    )
  })
})

describe('UsersPage — create user modal', () => {
  beforeEach(() => {
    mockUsersLoaded([])
    mockNoopMutations()
  })

  it('opens create user modal on button click', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /create user/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Create user' })).toBeInTheDocument()
  })

  it('closes modal on cancel', async () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /create user/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    })
  })

  it('calls createMutation.mutate with username, password, and roles on submit', () => {
    const mutateMock = vi.fn()
    vi.mocked(useCreateUser).mockReturnValue({
      mutate: mutateMock,
      isPending: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useCreateUser>)

    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /create user/i }))

    fireEvent.change(screen.getByLabelText(/username/i), { target: { value: 'newuser' } })
    fireEvent.change(screen.getByLabelText(/password/i), { target: { value: 'securepass' } })
    fireEvent.click(screen.getByLabelText(/operator/i))

    const form = document.querySelector('#create-user-form') as HTMLFormElement
    fireEvent.submit(form)

    expect(mutateMock).toHaveBeenCalledWith(
      { username: 'newuser', password: 'securepass', roles: ['operator'] },
      expect.any(Object),
    )
  })

  it('shows spinner when create is pending', () => {
    vi.mocked(useCreateUser).mockReturnValue({
      mutate: vi.fn(),
      isPending: true,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useCreateUser>)

    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /create user/i }))
    const submitBtn = screen.getByRole('button', { name: /creating/i })
    expect(submitBtn).toBeDisabled()
  })
})
