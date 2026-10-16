import type { Meta, StoryObj } from '@storybook/react-vite'
import '@/tokens.css'
import { StartupFaultBanner } from './StartupFaultBanner'
import type { ApiPluginInstanceStartupFault } from '@/api/types'

const FAILING: ApiPluginInstanceStartupFault = {
  failures: 4,
  last_error: 'start container: open sysctl net.ipv6.conf.all.disable_ipv6: no such file or directory',
  last_generation: 4,
  last_failed_at: '2026-10-01T12:00:00Z',
  next_attempt_at: '2026-10-01T12:01:20Z',
  attempt_in_progress: false,
  serving_generation: null,
}

const NOW = new Date('2026-10-01T12:00:30Z')

const meta: Meta<typeof StartupFaultBanner> = {
  title: 'Admin/StartupFaultBanner',
  component: StartupFaultBanner,
  args: { now: NOW },
}

export default meta
type Story = StoryObj<typeof StartupFaultBanner>

export const NothingServing: Story = { args: { fault: FAILING } }

export const FailedUpgradeStillServing: Story = {
  args: {
    fault: {
      ...FAILING,
      failures: 2,
      last_error: 'health gate: container exited with code 1 before becoming healthy',
      last_generation: 7,
      serving_generation: 5,
    },
  },
}

export const RetryInProgress: Story = { args: { fault: { ...FAILING, attempt_in_progress: true } } }
