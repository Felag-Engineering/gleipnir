import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { ThemeToggle } from './ThemeToggle'

// ThemeToggle reads from and writes to localStorage via useTheme. The active
// theme is reflected in the Storybook toolbar (preview.ts globalTypes.theme),
// but the toggle button itself tracks its own state independently. Both
// Expanded and Compact stories are rendered explicitly — do not rely on the
// toolbar to drive the component's internal state.
const meta: Meta<typeof ThemeToggle> = {
  title: 'Layout/ThemeToggle',
  component: ThemeToggle,
}

export default meta
type Story = StoryObj<typeof ThemeToggle>

export const Expanded: Story = {
  args: {
    compact: false,
  },
}

export const Compact: Story = {
  args: {
    compact: true,
  },
}
