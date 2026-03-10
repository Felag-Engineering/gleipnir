import type { Meta, StoryObj } from '@storybook/react-vite';
import SkeletonBlock from './SkeletonBlock';
import '../../tokens.css';
import styles from './SkeletonBlock.stories.module.css';

const meta: Meta<typeof SkeletonBlock> = {
  title: 'Shared/SkeletonBlock',
  component: SkeletonBlock,
  argTypes: {
    width: { control: 'text' },
    height: { control: 'text' },
    borderRadius: { control: 'text' },
  },
};

export default meta;
type Story = StoryObj<typeof SkeletonBlock>;

export const Default: Story = {
  args: { width: 200, height: 16 },
};

export const CardShaped: Story = {
  args: { width: 320, height: 180, borderRadius: 8 },
};

export const RowShaped: Story = {
  args: { width: '100%', height: 48, borderRadius: 4 },
};

export const Circle: Story = {
  args: { width: 40, height: 40, borderRadius: '50%' },
};

export const CardLayout: Story = {
  render: () => (
    <div className={styles.cardLayout}>
      <SkeletonBlock width="100%" height={180} borderRadius={8} />
      <SkeletonBlock width="60%" height={16} />
      <SkeletonBlock width="100%" height={12} />
      <SkeletonBlock width="80%" height={12} />
    </div>
  ),
};

export const ListLayout: Story = {
  render: () => (
    <div className={styles.listLayout}>
      {[1, 2, 3, 4].map((i) => (
        <SkeletonBlock key={i} width="100%" height={48} />
      ))}
    </div>
  ),
};
