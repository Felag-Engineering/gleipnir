import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { PageHeader } from './PageHeader'
import styles from './PageHeader.stories.module.css'

const meta: Meta<typeof PageHeader> = {
  title: 'Components/PageHeader',
  component: PageHeader,
}

export default meta
type Story = StoryObj<typeof PageHeader>

export const Default: Story = {
  args: {
    title: 'Agents',
  },
}

export const WithActions: Story = {
  args: {
    title: 'Agents',
    children: (
      <button className={styles.primaryAction}>
        New Agent
      </button>
    ),
  },
}

export const LongTitle: Story = {
  args: {
    title: 'Infrastructure Monitoring and Alerting Agents for Production Environment',
  },
}
