import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { CreateUserModal } from './CreateUserModal'

const meta: Meta<typeof CreateUserModal> = {
  title: 'UsersPage/CreateUserModal',
  component: CreateUserModal,
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
    onClose: () => {},
    onSubmit: (() => {}) as never,
    isPending: true,
    error: null,
  },
}
