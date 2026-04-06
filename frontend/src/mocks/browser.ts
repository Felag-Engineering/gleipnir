import { setupWorker } from 'msw/browser'
import { defaultHandlers } from './handlers'

// The MSW browser worker used in Storybook. Handlers registered here act as
// global defaults for every story. Per-story overrides can be applied via
// worker.use(...handlers) inside a story decorator, but must be cleaned up
// with worker.resetHandlers() in the decorator's cleanup to avoid cross-story
// pollution.
export const worker = setupWorker(...defaultHandlers)
