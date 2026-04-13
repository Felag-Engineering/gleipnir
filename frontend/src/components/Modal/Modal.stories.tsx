import type { Meta, StoryObj } from '@storybook/react-vite'
import { fn } from 'storybook/test'
import '@/tokens.css'
import { Modal } from './Modal'

const meta: Meta<typeof Modal> = {
  title: 'Components/Modal',
  component: Modal,
  args: {
    onClose: fn(),
  },
  parameters: {
    layout: 'fullscreen',
  },
}

export default meta
type Story = StoryObj<typeof Modal>

export const Default: Story = {
  args: {
    title: 'Confirm Action',
    children: <p style={{ margin: 0, color: 'var(--text-second)' }}>Are you sure you want to proceed?</p>,
  },
}

export const WithFooter: Story = {
  args: {
    title: 'Delete Agent',
    children: (
      <p style={{ margin: 0, color: 'var(--text-second)' }}>
        This will permanently delete the agent and all associated run history. This action cannot be undone.
      </p>
    ),
    footer: (
      <div style={{ display: 'flex', gap: '8px', justifyContent: 'flex-end' }}>
        <button
          style={{
            padding: '6px 16px',
            background: 'var(--bg-elevated)',
            color: 'var(--text-primary)',
            border: '1px solid var(--border-subtle)',
            borderRadius: '4px',
            cursor: 'pointer',
          }}
        >
          Cancel
        </button>
        <button
          style={{
            padding: '6px 16px',
            background: 'var(--color-red)',
            color: '#fff',
            border: 'none',
            borderRadius: '4px',
            cursor: 'pointer',
          }}
        >
          Delete
        </button>
      </div>
    ),
  },
}

export const LongContent: Story = {
  args: {
    title: 'System Prompt Preview',
    children: (
      <div style={{ color: 'var(--text-second)', lineHeight: 1.6 }}>
        {Array.from({ length: 8 }, (_, i) => (
          <p key={i} style={{ marginTop: i === 0 ? 0 : '12px' }}>
            You are an autonomous infrastructure agent responsible for monitoring and maintaining
            the production environment. Your primary directives are to ensure uptime, respond to
            alerts, and escalate issues that exceed your authority level to the on-call operator.
          </p>
        ))}
      </div>
    ),
  },
}

export const EmptyBody: Story = {
  args: {
    title: 'Empty Modal',
    children: null,
  },
}
