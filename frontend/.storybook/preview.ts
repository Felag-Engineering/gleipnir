import type { Preview } from '@storybook/react-vite'

const preview: Preview = {
  parameters: {
    backgrounds: {
      default: 'gleipnir-dark',
      values: [
        { name: 'gleipnir-dark', value: '#0f1117' },
        { name: 'light', value: '#ffffff' },
      ],
    },
    controls: {
      matchers: {
        color: /(background|color)$/i,
        date: /Date$/i,
      },
    },
    a11y: {
      test: 'todo',
    },
  },
};

export default preview;
