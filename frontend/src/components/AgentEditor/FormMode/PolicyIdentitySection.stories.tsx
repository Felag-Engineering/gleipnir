import type { Meta, StoryObj } from '@storybook/react-vite';
import { fn } from 'storybook/test';
import { useState } from 'react';
import '@/tokens.css';
import { PolicyIdentitySection } from './PolicyIdentitySection';
import type { IdentityFormState } from './types';
import decoratorStyles from './PolicyIdentitySection.stories.module.css';

const meta: Meta<typeof PolicyIdentitySection> = {
  title: 'PolicyEditor/FormMode/PolicyIdentitySection',
  component: PolicyIdentitySection,
  decorators: [
    (Story) => (
      <div className={decoratorStyles.decorator}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof PolicyIdentitySection>;

export const Empty: Story = {
  args: {
    value: { name: '', description: '', folder: '' },
    onChange: fn(),
  },
};

export const Filled: Story = {
  args: {
    value: {
      name: 'deploy-on-push',
      description: 'Deploy app when webhook fires',
      folder: 'deployments',
    },
    onChange: fn(),
  },
};

export const WithFolderSuggestions: Story = {
  args: {
    value: { name: 'my-agent', description: '', folder: '' },
    onChange: fn(),
    existingFolders: ['deployments', 'monitoring', 'automation'],
  },
};

function InteractiveIdentitySection() {
  const [value, setValue] = useState<IdentityFormState>({
    name: 'my-policy',
    description: '',
    folder: '',
  });
  return <PolicyIdentitySection value={value} onChange={setValue} />;
}

export const Interactive: Story = {
  render: () => <InteractiveIdentitySection />,
};
