import { setupServer } from 'msw/node'
import { handlers } from './handlers'

// Node-side MSW server used by Vitest. Tests may call `server.use(...)`
// to override the default handlers for specific scenarios.
export const server = setupServer(...handlers)
