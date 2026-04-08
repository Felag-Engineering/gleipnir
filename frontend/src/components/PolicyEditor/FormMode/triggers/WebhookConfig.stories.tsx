import type { Meta, StoryObj } from '@storybook/react-vite';
import '@/tokens.css';
import { WebhookConfig } from './WebhookConfig';
import decoratorStyles from '../TriggerSection.stories.module.css';

const meta: Meta<typeof WebhookConfig> = {
  title: 'PolicyEditor/FormMode/triggers/WebhookConfig',
  component: WebhookConfig,
  decorators: [
    (Story) => (
      <div className={decoratorStyles.decorator}>
        <Story />
      </div>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof WebhookConfig>;

export const WithPolicyId: Story = {
  args: {
    policyId: 'abc-123',
  },
};

export const NewPolicy: Story = {
  args: {
    // No policyId — Copy button is disabled and URL shows placeholder.
  },
};
