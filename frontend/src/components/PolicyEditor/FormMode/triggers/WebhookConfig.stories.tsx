import type { Meta, StoryObj } from '@storybook/react-vite';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { http, HttpResponse } from 'msw';
import { fn } from 'storybook/test';
import '@/tokens.css';
import { WebhookConfig } from './WebhookConfig';
import decoratorStyles from '../TriggerSection.stories.module.css';

const FAKE_SECRET = 'a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2';
const POLICY_ID = 'pol-abc-123';

function makeQueryClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

const meta: Meta<typeof WebhookConfig> = {
  title: 'PolicyEditor/FormMode/triggers/WebhookConfig',
  component: WebhookConfig,
  decorators: [
    (Story) => (
      <QueryClientProvider client={makeQueryClient()}>
        <div className={decoratorStyles.decorator}>
          <Story />
        </div>
      </QueryClientProvider>
    ),
  ],
};

export default meta;
type Story = StoryObj<typeof WebhookConfig>;

// --- HMACWithSecret ---
// Simulates a saved policy with a rotated HMAC secret. The "Show" button will
// reveal the 64-hex secret returned by the GET webhook/secret endpoint.
export const HMACWithSecret: Story = {
  args: {
    policyId: POLICY_ID,
    value: { type: 'webhook', auth: 'hmac' },
    onChange: fn(),
  },
  parameters: {
    msw: {
      handlers: [
        http.get(`/api/v1/policies/${POLICY_ID}/webhook/secret`, () =>
          HttpResponse.json({ data: { secret: FAKE_SECRET } }),
        ),
      ],
    },
  },
};

// --- HMACNoSecretYet ---
// Simulates a saved policy where no secret has been rotated yet. Shows the
// "Generate initial secret" call-to-action.
export const HMACNoSecretYet: Story = {
  args: {
    policyId: POLICY_ID,
    value: { type: 'webhook', auth: 'hmac' },
    onChange: fn(),
  },
  parameters: {
    msw: {
      handlers: [
        http.get(`/api/v1/policies/${POLICY_ID}/webhook/secret`, () =>
          HttpResponse.json({ error: 'no_secret', detail: 'no webhook secret has been set' }, { status: 404 }),
        ),
        http.post(`/api/v1/policies/${POLICY_ID}/webhook/rotate`, () =>
          HttpResponse.json({ data: { secret: FAKE_SECRET } }),
        ),
      ],
    },
  },
};

// --- Bearer ---
// Shows the Bearer token mode with the snippet.
export const Bearer: Story = {
  args: {
    policyId: POLICY_ID,
    value: { type: 'webhook', auth: 'bearer' },
    onChange: fn(),
  },
  parameters: {
    msw: {
      handlers: [
        http.get(`/api/v1/policies/${POLICY_ID}/webhook/secret`, () =>
          HttpResponse.json({ data: { secret: FAKE_SECRET } }),
        ),
      ],
    },
  },
};

// --- NoneInsecure ---
// Shows the warning banner and no snippet.
export const NoneInsecure: Story = {
  args: {
    policyId: POLICY_ID,
    value: { type: 'webhook', auth: 'none' },
    onChange: fn(),
  },
};

// --- NewAgent ---
// No policyId — copy button disabled, no secret section shown.
export const NewAgent: Story = {
  args: {
    value: { type: 'webhook', auth: 'hmac' },
    onChange: fn(),
  },
};
