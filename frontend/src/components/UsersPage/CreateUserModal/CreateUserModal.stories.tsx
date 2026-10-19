import type { Meta, StoryObj } from '@storybook/react-vite'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { http, HttpResponse } from 'msw'
import '@/tokens.css'
import { CreateUserModal } from './CreateUserModal'

function makeQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
}

const meta: Meta<typeof CreateUserModal> = {
  title: 'UsersPage/CreateUserModal',
  component: CreateUserModal,
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient()}>
        <Story />
      </QueryClientProvider>
    ),
  ],
  parameters: {
    msw: {
      handlers: [
        http.get('/api/v1/admin/plugin-instances', () => HttpResponse.json({ data: [] })),
        http.get('/api/v1/users/:id/identities', () => HttpResponse.json({ data: [] })),
      ],
    },
  },
}

export default meta
type Story = StoryObj<typeof CreateUserModal>

// onSubmit is cast as `never` throughout because Storybook's arg type inference
// can't resolve the discriminated union — the actual signature varies by `mode`.
// The cast is safe here since stories don't exercise the submit path.

export const CreateIdle: Story = {
  args: {
    mode: 'create',
    onClose: () => {},
    onSubmit: (() => {}) as never,
    isPending: false,
    error: null,
  },
}

export const CreatePending: Story = {
  args: {
    mode: 'create',
    onClose: () => {},
    onSubmit: (() => {}) as never,
    isPending: true,
    error: null,
  },
}

export const CreateWithError: Story = {
  args: {
    mode: 'create',
    onClose: () => {},
    onSubmit: (() => {}) as never,
    isPending: false,
    error: { message: 'Username already exists' } as never,
  },
}

export const EditIdle: Story = {
  args: {
    mode: 'edit',
    initialRole: 'operator',
    userId: 'u1',
    onClose: () => {},
    onSubmit: (() => {}) as never,
    isPending: false,
    error: null,
  },
}

export const EditPending: Story = {
  args: {
    mode: 'edit',
    initialRole: 'admin',
    userId: 'u1',
    onClose: () => {},
    onSubmit: (() => {}) as never,
    isPending: true,
    error: null,
  },
}

export const EditWithLinkedInstances: Story = {
  args: {
    mode: 'edit',
    initialRole: 'operator',
    userId: 'u1',
    onClose: () => {},
    onSubmit: (() => {}) as never,
    isPending: false,
    error: null,
  },
  parameters: {
    msw: {
      handlers: [
        http.get('/api/v1/admin/plugin-instances', () =>
          HttpResponse.json({
            data: [
              { id: 'i1', instance_name: 'Team chat', identity_link_methods: ['admin_set'] },
              { id: 'i2', instance_name: 'Ops chat', identity_link_methods: ['admin_set'] },
            ],
          }),
        ),
        http.get('/api/v1/users/:id/identities', () =>
          HttpResponse.json({
            data: [
              {
                instance_id: 'i1',
                instance_name: 'Team chat',
                external_user_id: 'U01ABC123DE',
                link_method: 'admin_set',
                verified_at: null,
                created_at: '2026-01-01T00:00:00Z',
              },
            ],
          }),
        ),
      ],
    },
  },
}
