import { useState } from 'react'
import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ResponseButtonsEditor } from './ResponseButtonsEditor'
import type { ResponseButton } from './ResponseButtonsEditor'

const meta: Meta<typeof ResponseButtonsEditor> = {
  title: 'Admin/AudienceEditor/ResponseButtonsEditor',
  component: ResponseButtonsEditor,
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 720, padding: '24px' }}>
        <Story />
      </div>
    ),
  ],
}

export default meta
type Story = StoryObj<typeof ResponseButtonsEditor>

// Controlled wrapper for stories so interactions update state.
function Controlled({ initial }: { initial: ResponseButton[] | undefined }) {
  const [value, setValue] = useState<ResponseButton[] | undefined>(initial)
  return <ResponseButtonsEditor value={value} onChange={setValue} />
}

export const Empty: Story = {
  render: () => <Controlled initial={undefined} />,
}

export const OneRow: Story = {
  render: () => (
    <Controlled
      initial={[{ option_id: 'approve', label: 'Approve', value: 'approved', style: 'primary' }]}
    />
  ),
}

export const MultipleRows: Story = {
  render: () => (
    <Controlled
      initial={[
        { option_id: 'approve', label: 'Approve', value: 'approved', style: 'primary' },
        { option_id: 'reject', label: 'Reject', value: 'rejected', style: 'danger' },
        { option_id: 'defer', label: 'Defer', value: 'deferred' },
      ]}
    />
  ),
}

export const AllStyles: Story = {
  render: () => (
    <Controlled
      initial={[
        { option_id: 'a', label: 'Default style', value: 'a' },
        { option_id: 'b', label: 'Default explicit', value: 'b', style: 'default' },
        { option_id: 'c', label: 'Primary', value: 'c', style: 'primary' },
        { option_id: 'd', label: 'Danger', value: 'd', style: 'danger' },
      ]}
    />
  ),
}

export const Disabled: Story = {
  render: () => (
    <ResponseButtonsEditor
      value={[
        { option_id: 'approve', label: 'Approve', value: 'approved', style: 'primary' },
        { option_id: 'reject', label: 'Reject', value: 'rejected', style: 'danger' },
      ]}
      onChange={() => {}}
      disabled
    />
  ),
}
