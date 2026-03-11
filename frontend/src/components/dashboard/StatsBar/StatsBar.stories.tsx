import type { Meta, StoryObj } from '@storybook/react-vite';
import '@/tokens.css';
import { StatsBar, makeDashboardStats } from './StatsBar';

const meta: Meta<typeof StatsBar> = {
  title: 'Dashboard/StatsBar',
  component: StatsBar,
};

export default meta;
type Story = StoryObj<typeof StatsBar>;

export const ActiveDashboard: Story = {
  args: { stats: makeDashboardStats(1, 2, 3, 23680) },
};

export const AllQuiet: Story = {
  args: { stats: makeDashboardStats(0, 0, 3, 23680) },
};

export const HighActivity: Story = {
  args: { stats: makeDashboardStats(5, 4, 8, 142300) },
};
