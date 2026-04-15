import type { Meta, StoryObj } from '@storybook/react-vite';
import { fn } from 'storybook/test';
import { MemoryRouter } from 'react-router-dom';
import '@/tokens.css';
import { EditorTopBar } from './EditorTopBar';

const meta: Meta<typeof EditorTopBar> = {
  title: 'PolicyEditor/EditorTopBar',
  component: EditorTopBar,
  decorators: [
    (Story) => (
      <MemoryRouter>
        <Story />
      </MemoryRouter>
    ),
  ],
  args: {
    onSave: fn(),
    onDeleteClick: fn(),
  },
};

export default meta;
type Story = StoryObj<typeof EditorTopBar>;

export const Clean: Story = {
  args: {
    policyName: 'deploy-on-push',
    canSave: false,
    isEditMode: true,
  },
};

export const SaveEnabled: Story = {
  args: {
    policyName: 'deploy-on-push',
    canSave: true,
    isEditMode: true,
  },
};

export const CreateMode: Story = {
  args: {
    policyName: 'New Agent',
    canSave: true,
    isEditMode: false,
  },
};

export const ManualTrigger: Story = {
  args: {
    policyName: 'on-demand-check',
    canSave: false,
    isEditMode: true,
    onRunNowClick: fn(),
  },
};
