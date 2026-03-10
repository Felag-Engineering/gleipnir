import type { Meta, StoryObj } from '@storybook/react-vite';
import { StatsBar, makeDashboardStats } from './StatsBar';
import { GLOBAL_STYLES } from './styles';

const meta: Meta<typeof StatsBar> = {
  title: 'Dashboard/StatsBar',
  component: StatsBar,
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 900, padding: 20 }}>
        <style>{GLOBAL_STYLES}</style>
        <Story />
      </div>
    ),
  ],
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
