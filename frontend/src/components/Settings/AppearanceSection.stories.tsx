import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { AppearanceSection } from './AppearanceSection'

const meta: Meta<typeof AppearanceSection> = {
  title: 'Settings/AppearanceSection',
  component: AppearanceSection,
}

export default meta
type Story = StoryObj<typeof AppearanceSection>

export const Default: Story = {}
