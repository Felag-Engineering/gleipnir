import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: '.',
  testMatch: '**/*.spec.ts',

  // 5 minutes — kitchen-sink runs an agent turn; 300s gives headroom for
  // Anthropic 5xx/529 backoff. Tight 120s deadlines have proven flaky.
  timeout: 300_000,

  expect: {
    timeout: 30_000,
  },

  // Two retries in CI for nightly reliability; expected flake rate ~1-2%.
  retries: process.env.CI ? 2 : 0,

  // Slack workspace tests share a channel and cannot run in parallel.
  workers: 1,

  reporter: process.env.CI
    ? [['github'], ['html', { open: 'never' }]]
    : 'list',

  use: {
    baseURL: process.env.GLEIPNIR_E2E_BASE_URL || 'http://localhost:8080',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },
});
