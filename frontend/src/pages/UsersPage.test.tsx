import React from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'

import UsersPage from './UsersPage'
import type { ApiUser } from '@/api/types'

// --- Mocks ---

vi.mock('@/hooks/queries/users')
vi.mock('@/hooks/mutations/users')

import { useUsers } from '@/hooks/queries/users'
import { useCreateUser } from '@/hooks/mutations/users'
import { useUpdateUser } from '@/hooks/mutations/users'

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
    const deactivateBtns = screen.getAllByRole('button', { name: /^deactivate$/i })
    expect(deactivateBtns.length).toBe(2)
  })

  it('shows highest role badge for each user', () => {
    renderPage()
    expect(screen.getByText('admin')).toBeInTheDocument()
    expect(screen.getByText('operator')).toBeInTheDocument()
  })

  it('shows an Edit button for each user row', () => {
    renderPage()
    const editBtns = screen.getAllByRole('button', { name: /^edit$/i })
    expect(editBtns.length).toBe(3)
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
    fireEvent.click(screen.getByRole('button', { name: /^deactivate$/i }))

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
    fireEvent.click(screen.getByRole('radio', { name: /auditor/i }))

    fireEvent.submit(document.querySelector('#user-modal-form') as HTMLFormElement)

    expect(mutateMock).toHaveBeenCalledWith(
      { username: 'newuser', password: 'securepass', roles: ['auditor'] },
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

  it('empty fields show inline errors, not native tooltip', () => {
    const mutateMock = vi.fn()
    vi.mocked(useCreateUser).mockReturnValue({
      mutate: mutateMock,
      isPending: false,
      error: null,
      reset: vi.fn(),
    } as unknown as ReturnType<typeof useCreateUser>)

    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /create user/i }))

    fireEvent.submit(document.querySelector('#user-modal-form') as HTMLFormElement)

    expect(screen.getAllByText(/username is required/i).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/password is required/i).length).toBeGreaterThan(0)

    expect(screen.getByLabelText(/username/i)).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByLabelText(/password/i)).toHaveAttribute('aria-invalid', 'true')

    expect(mutateMock).not.toHaveBeenCalled()
  })
})

describe('UsersPage — create modal role selection', () => {
  beforeEach(() => {
    mockUsersLoaded([])
    mockNoopMutations()
  })

  it('shows placeholder text when no role is selected', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /create user/i }))
    expect(screen.getByText(/select a role to see its permissions/i)).toBeInTheDocument()
  })

  it('selecting a radio marks only that role as checked', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /create user/i }))

    fireEvent.click(screen.getByRole('radio', { name: /operator/i }))

    expect(screen.getByRole('radio', { name: /operator/i })).toBeChecked()
    expect(screen.getByRole('radio', { name: /admin/i })).not.toBeChecked()
    expect(screen.getByRole('radio', { name: /approver/i })).not.toBeChecked()
    expect(screen.getByRole('radio', { name: /auditor/i })).not.toBeChecked()
  })

  it('switching radio selection deselects previous choice', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /create user/i }))

    fireEvent.click(screen.getByRole('radio', { name: /admin/i }))
    fireEvent.click(screen.getByRole('radio', { name: /auditor/i }))

    expect(screen.getByRole('radio', { name: /auditor/i })).toBeChecked()
    expect(screen.getByRole('radio', { name: /admin/i })).not.toBeChecked()
  })

  it('permissions panel updates when radio selection changes', () => {
    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /create user/i }))

    fireEvent.click(screen.getByRole('radio', { name: /operator/i }))

    expect(screen.getAllByText(/trigger runs, manage policies/i).length).toBeGreaterThan(0)
  })

  it('submitting with operator radio sends all implied roles to the API', () => {
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
    fireEvent.change(screen.getByLabelText(/password/i), { target: { value: 'pass' } })
    fireEvent.click(screen.getByRole('radio', { name: /operator/i }))
    fireEvent.submit(document.querySelector('#user-modal-form') as HTMLFormElement)

    expect(mutateMock).toHaveBeenCalledWith(
      expect.objectContaining({
        roles: expect.arrayContaining(['operator', 'approver', 'auditor']),
      }),
      expect.any(Object),
    )
    expect(mutateMock.mock.calls[0][0].roles).not.toContain('admin')
  })
})

describe('UsersPage — edit user modal', () => {
  it('opens edit modal with correct title when Edit is clicked', () => {
    mockUsersLoaded([USER_OPERATOR])
    mockNoopMutations()

    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /^edit$/i }))

    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Edit user' })).toBeInTheDocument()
  })

  it('pre-selects the user\'s current highest role in the edit modal', () => {
    mockUsersLoaded([USER_OPERATOR])
    mockNoopMutations()

    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /^edit$/i }))

    expect(screen.getByRole('radio', { name: /operator/i })).toBeChecked()
    expect(screen.getByRole('radio', { name: /admin/i })).not.toBeChecked()
  })

  it('does not show username or password fields in edit modal', () => {
    mockUsersLoaded([USER_OPERATOR])
    mockNoopMutations()

    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /^edit$/i }))

    expect(screen.queryByLabelText(/username/i)).not.toBeInTheDocument()
    expect(screen.queryByLabelText(/password/i)).not.toBeInTheDocument()
  })

  it('calls updateMutation.mutate with expanded roles on save', () => {
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
    fireEvent.click(screen.getByRole('button', { name: /^edit$/i }))

    // Change role to admin
    fireEvent.click(screen.getByRole('radio', { name: /admin/i }))
    fireEvent.submit(document.querySelector('#user-modal-form') as HTMLFormElement)

    expect(mutateMock).toHaveBeenCalledWith(
      expect.objectContaining({
        id: 'u2',
        roles: expect.arrayContaining(['admin', 'operator', 'approver', 'auditor']),
      }),
      expect.any(Object),
    )
  })

  it('closes edit modal on cancel', async () => {
    mockUsersLoaded([USER_OPERATOR])
    mockNoopMutations()

    renderPage()
    fireEvent.click(screen.getByRole('button', { name: /^edit$/i }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    })
  })
})
