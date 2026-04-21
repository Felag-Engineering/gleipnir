import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { EncryptionKeyNotice } from './EncryptionKeyNotice'

const meta: Meta<typeof EncryptionKeyNotice> = {
  title: 'Admin/EncryptionKeyNotice',
  component: EncryptionKeyNotice,
}

export default meta
type Story = StoryObj<typeof EncryptionKeyNotice>

export const Default: Story = {
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 720, padding: 24 }}>
        <Story />
      </div>
    ),
  ],
}
