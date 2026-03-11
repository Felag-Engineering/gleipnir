import type { Meta, StoryObj } from '@storybook/react-vite';
import { fn } from 'storybook/test';
import '../../../tokens.css';
import { EditorTopBar } from './EditorTopBar';

const meta: Meta<typeof EditorTopBar> = {
  title: 'PolicyEditor/EditorTopBar',
  component: EditorTopBar,
  argTypes: {
    mode: { control: 'select', options: ['form', 'yaml'] },
  },
  args: {
    onModeChange: fn(),
    onSave: fn(),
    onDelete: fn(),
  },
};

export default meta;
type Story = StoryObj<typeof EditorTopBar>;

export const Clean: Story = {
  args: {
    policyName: 'deploy-on-push',
    isDirty: false,
    mode: 'form',
    canSave: false,
    isEditMode: true,
  },
};

export const Dirty: Story = {
  args: {
    policyName: 'deploy-on-push',
    isDirty: true,
    mode: 'form',
    canSave: true,
    isEditMode: true,
  },
};

export const YamlInvalid: Story = {
  args: {
    policyName: 'deploy-on-push',
    isDirty: true,
    mode: 'yaml',
    canSave: false,
    isEditMode: true,
  },
};

export const CreateMode: Story = {
  args: {
    policyName: 'New Policy',
    isDirty: true,
    mode: 'form',
    canSave: true,
    isEditMode: false,
  },
};
