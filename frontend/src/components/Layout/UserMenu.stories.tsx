import type { Meta, StoryObj } from '@storybook/react-vite'
import { fn } from 'storybook/test'
import { MemoryRouter } from 'react-router-dom'
import '@/tokens.css'
import { UserMenu } from './UserMenu'
import styles from './UserMenu.stories.module.css'

const meta: Meta<typeof UserMenu> = {
  title: 'Layout/UserMenu',
  component: UserMenu,
  decorators: [(Story) => <MemoryRouter><Story /></MemoryRouter>],
  args: {
    onClose: fn(),
  },
}

export default meta
type Story = StoryObj<typeof UserMenu>

export const Open: Story = {
  args: {
    open: true,
  },
}

export const Closed: Story = {
  args: {
    open: false,
  },
}

// Positions the menu in the bottom-right corner to exercise that layout context.
// The outer MemoryRouter comes from the meta-level decorator, so this decorator
// just adds the positioning wrapper without adding a second router.
export const OpenInCorner: Story = {
  decorators: [
    (Story) => (
      <div className={styles.cornerWrapper}>
        <Story />
      </div>
    ),
  ],
  args: {
    open: true,
  },
}
