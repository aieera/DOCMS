// Startup configuration for the collaboration service.
//
// Required variables are validated once, before any client (Redis, NATS,
// Postgres) is created, so a misconfigured deployment exits immediately
// with a clear message instead of crash-looping later or silently using a
// hard-coded development DSN (R-16, CB-12).

// Largest WebSocket frame the server accepts (ws `maxPayload`). Yjs sync
// frames for a document larger than this are not expected; anything
// bigger is closed with 1009 before it is buffered.
export const MAX_PAYLOAD_BYTES = 1024 * 1024

export class ConfigError extends Error {
  constructor(problems) {
    super(problems.join('; '))
    this.name = 'ConfigError'
    this.problems = problems
  }
}

/**
 * Validate and return the service configuration.
 * @param {Record<string, string|undefined>} env
 */
export function loadConfig(env = process.env) {
  const problems = []
  const gatewaySecret = env.SEDOC_GATEWAY_SECRET || env.VAULTDMS_GATEWAY_SECRET || ''
  if (!gatewaySecret) {
    problems.push('SEDOC_GATEWAY_SECRET is required (shared gateway signature secret)')
  }
  const databaseUrl = env.SEDOC_DATABASE_URL || ''
  if (!databaseUrl) {
    problems.push('SEDOC_DATABASE_URL is required (Yjs snapshot store); there is no default')
  }
  if (problems.length) throw new ConfigError(problems)
  return {
    port: parseInt(env.WS_PORT || '8083', 10),
    redisUrl: env.REDIS_URL || 'redis://localhost:6379',
    natsUrl: env.SEDOC_NATS_URL || env.VAULTDMS_NATS_URL || env.NATS_URL || 'nats://nats:4222',
    databaseUrl,
    gatewaySecret,
    gatewaySecretPrev: env.SEDOC_GATEWAY_SECRET_PREV || '',
  }
}
