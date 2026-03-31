import type { Preview } from '@storybook/react-vite'
import '../src/tokens.css'

const preview: Preview = {
  globalTypes: {
    theme: {
      description: 'Theme',
      toolbar: {
        title: 'Theme',
        icon: 'paintbrush',
        items: ['system', 'light', 'dark'],
        dynamicTitle: true,
      },
    },
  },
  decorators: [
    (Story, context) => {
      const theme = context.globals['theme'] || 'dark'
      document.documentElement.setAttribute('data-theme', theme)
      // Auto-switch Storybook canvas background so users don't have to change two things
      const isDark =
        theme === 'dark' ||
        (theme === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)
      document.body.style.backgroundColor = isDark ? '#0f1117' : '#f8fafc'
      return Story()
    },
  ],
  parameters: {
    backgrounds: {
      default: 'gleipnir-dark',
      values: [
        { name: 'gleipnir-dark', value: '#0f1117' },
        { name: 'gleipnir-light', value: '#f8fafc' },
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
