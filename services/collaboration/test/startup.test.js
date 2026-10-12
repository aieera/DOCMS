/**
 * R-16 / CB-12 — process startup and transport hardening:
 *   - the process exits non-zero when SEDOC_DATABASE_URL (or the gateway
 *     secret) is unset, instead of falling back to a hard-coded dev DSN;
 *   - the WebSocket server caps frames at 1 MiB (maxPayload);
 *   - a malformed Redis pub/sub payload cannot throw out of the
 *     subscriber callback (an uncaught JSON.parse used to kill the pod).
 */

import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import path from 'node:path'
import { describe, expect, test, onTestFinished } from 'vitest'
import { WebSocket } from 'ws'

import { createFakeRedisBus } from './fake-redis.js'
import { loadConfig, MAX_PAYLOAD_BYTES } from '../src/config.js'
import { createCollabServer, handleRedisDocMessage } from '../src/server.js'

const serviceDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')

/** Run `node src/index.js` with exactly `env`; resolve {code, stderr}. */
function runIndex(env, killAfterMs = 4000) {
  return new Promise((resolve) => {
    const child = spawn(process.execPath, ['src/index.js'], { cwd: serviceDir, env })
    let stderr = ''
    child.stderr.on('data', (d) => { stderr += d })
    const timer = setTimeout(() => child.kill('SIGKILL'), killAfterMs)
    child.on('exit', (code, signal) => {
      clearTimeout(timer)
      resolve({ code, signal, stderr })
    })
  })
}

// Never touch the live dev stack: port 0, unroutable Redis/NATS.
const baseEnv = {
  PATH: process.env.PATH,
  WS_PORT: '0',
  REDIS_URL: 'redis://127.0.0.1:1',
  SEDOC_NATS_URL: 'nats://127.0.0.1:1',
}

describe('startup configuration', () => {
  test('exits 1 when SEDOC_DATABASE_URL is unset (no dev DSN fallback)', async () => {
    const res = await runIndex({ ...baseEnv, SEDOC_GATEWAY_SECRET: 'test-gateway-secret', SEDOC_ALLOWED_ORIGINS: 'https://app.sedoc.test' })
    expect(res.signal).toBeNull()
    expect(res.code).toBe(1)
    expect(res.stderr).toContain('SEDOC_DATABASE_URL')
  })

  test('exits 1 when SEDOC_GATEWAY_SECRET is unset', async () => {
    const res = await runIndex({ ...baseEnv, SEDOC_DATABASE_URL: 'postgres://u:p@127.0.0.1:1/x', SEDOC_ALLOWED_ORIGINS: 'https://app.sedoc.test' })
    expect(res.signal).toBeNull()
    expect(res.code).toBe(1)
    expect(res.stderr).toContain('SEDOC_GATEWAY_SECRET')
  })

  test('loadConfig reports every missing required variable', () => {
    expect(() => loadConfig({})).toThrow(/SEDOC_GATEWAY_SECRET[\s\S]*SEDOC_DATABASE_URL[\s\S]*SEDOC_ALLOWED_ORIGINS/)
    const cfg = loadConfig({
      SEDOC_GATEWAY_SECRET: 's',
      SEDOC_DATABASE_URL: 'postgres://x',
      SEDOC_ALLOWED_ORIGINS: 'https://app.example.com, http://localhost:3000',
    })
    expect(cfg.databaseUrl).toBe('postgres://x')
    expect(cfg.gatewaySecret).toBe('s')
    expect(cfg.allowedOrigins).toEqual(['https://app.example.com', 'http://localhost:3000'])
  })

  test('exits 1 when SEDOC_ALLOWED_ORIGINS is unset (cookie-mode upgrades need it)', async () => {
    const res = await runIndex({
      ...baseEnv,
      SEDOC_GATEWAY_SECRET: 'test-gateway-secret',
      SEDOC_DATABASE_URL: 'postgres://u:p@127.0.0.1:1/x',
    })
    expect(res.signal).toBeNull()
    expect(res.code).toBe(1)
    expect(res.stderr).toContain('SEDOC_ALLOWED_ORIGINS')
  })

  test('SEDOC_ALLOWED_ORIGINS entries must be exact origins', () => {
    const base = { SEDOC_GATEWAY_SECRET: 's', SEDOC_DATABASE_URL: 'postgres://x' }
    for (const bad of ['*', 'https://app.example.com/', 'app.example.com', 'https://app.example.com/path', 'null', ' , ']) {
      expect(() => loadConfig({ ...base, SEDOC_ALLOWED_ORIGINS: bad }), bad).toThrow(/SEDOC_ALLOWED_ORIGINS/)
    }
  })
})

async function startServer() {
  const bus = createFakeRedisBus()
  const server = createCollabServer({
    port: 0,
    host: '127.0.0.1',
    redisPub: bus.client(),
    redisSub: bus.client(),
    redisStore: bus.client(),
  })
  await server.listening
  onTestFinished(() => server.close())
  return server
}

describe('transport limits', () => {
  test('maxPayload is 1 MiB and an oversized frame closes the socket with 1009', async () => {
    expect(MAX_PAYLOAD_BYTES).toBe(1024 * 1024)
    const server = await startServer()
    expect(server.wss.options.maxPayload).toBe(MAX_PAYLOAD_BYTES)

    const ws = new WebSocket(`ws://127.0.0.1:${server.port}/ws`, {
      headers: { 'X-Gateway-Signature': 'test-gateway-secret' },
    })
    ws.on('error', () => {})
    await new Promise((resolve, reject) => { ws.once('open', resolve); ws.once('error', reject) })
    const closed = new Promise((resolve) => ws.once('close', (code) => resolve(code)))
    ws.send(Buffer.alloc(MAX_PAYLOAD_BYTES + 1, 0x61))
    expect(await closed).toBe(1009)
  })

  test('a malformed Redis doc-room payload is dropped, not thrown', () => {
    const delivered = []
    const connections = { broadcastToDoc: (...a) => delivered.push(a) }
    expect(() => handleRedisDocMessage(connections, 'dms:t1:doc:d1', '{not json')).not.toThrow()
    expect(() => handleRedisDocMessage(connections, 'dms:t1:doc:d1', 'null')).not.toThrow()
    expect(delivered).toHaveLength(0)
    handleRedisDocMessage(connections, 'dms:t1:doc:d1', JSON.stringify({ type: 'x', _senderUserId: 'u' }))
    expect(delivered).toEqual([['t1', 'd1', { type: 'x', _senderUserId: 'u' }, 'u']])
  })
})
