// ADR 0074 — urql client wired to the VaultDMS GraphQL gateway.
//
// Persisted queries are enforced: every operation submits its
// SHA-256 hash via the standard APQ extensions envelope. Production
// builds reject inline queries; dev builds tolerate them through the
// gateway's loopback `?dev=1` escape hatch (used by introspection /
// curl smoke tests, never by this client).
//
// Why no fallback to inline query: the @urql/exchange-persisted
// default is "send hash, fall back to inline on
// PersistedQueryNotFound" — fine for Apollo APQ servers but defeats
// the point of our build-time allow-list. We pass
// `enforcePersistedQueries: true` to disable the fallback. Unknown
// hash → the response error renders as a query failure rather than
// an undetected inline-query leak.

import { Client, fetchExchange, cacheExchange } from 'urql'
import { persistedExchange } from '@urql/exchange-persisted'

import { useAuthStore } from '@/store/authStore'
import type { PersistedOperation } from './graphql-operations'

// authHeaders mirrors the X-Auth-* headers the axios api client
// stamps in src/api/client.ts. The graphql-gateway requires them
// for the per-request tenant + identity context — without them
// every call returns 401 "tenant + user identity required".
function authHeaders(): Record<string, string> {
  const { tenantId, user } = useAuthStore.getState()
  const h: Record<string, string> = { 'Content-Type': 'application/json' }
  if (tenantId) {
    h['X-Auth-Tenant-ID'] = tenantId
    h['X-Tenant-ID'] = tenantId
  }
  if (user?.id) h['X-Auth-User-ID'] = user.id
  if (user?.id) h['X-User-ID'] = user.id
  if (user?.role) h['X-User-Role'] = user.role
  return h
}

// generateHash is the function urql calls to derive the SHA-256 of a
// query document. We *don't* actually let it derive — every operation
// the app issues comes from a precomputed PersistedOperation
// (graphql-operations.ts), and the request helper below stamps the
// hash directly. The function is wired anyway so urql is happy with
// the exchange config; it would only fire if a caller bypassed the
// helper and used a raw `gql` template, which we want to surface as
// a hash mismatch + 400 rather than a silent inline query.
async function generateHash(_query: string, document: unknown): Promise<string> {
  // urql passes its TypedDocumentNode here — read its stable
  // cache key to produce a deterministic-but-wrong hash, which the
  // backend will reject with PersistedQueryNotFound. That surfaces
  // the bypass instead of silently retrying with the inline query.
  const key = (document as { __key?: number } | undefined)?.__key
  return `urql-unindexed-${key ?? 'unknown'}`
}

const httpEndpoint = '/api/v1/graphql'

export const graphqlClient = new Client({
  url: httpEndpoint,
  exchanges: [
    cacheExchange,
    persistedExchange({
      generateHash,
      // Always send the hash, never the query. The gateway returns
      // 400 PersistedQueryNotFound on miss; we want that surfaced as
      // a hard error rather than silently retrying with the inline
      // query.
      enforcePersistedQueries: true,
      // Keep POST — our gateway doesn't accept GraphQL via GET.
      preferGetForPersistedQueries: false,
    }),
    fetchExchange,
  ],
  // Send cookies + the X-Auth-* identity headers the gateway requires
  // (mirrors the axios api client's interceptor in api/client.ts).
  fetchOptions: () => ({
    credentials: 'include',
    headers: authHeaders(),
  }),
})

// runPersistedQuery is the manual fetch helper for code paths that
// don't use urql's React hooks (background polling, e2e fixtures,
// devtools). Issues the hash directly rather than relying on urql's
// generateHash, so the wire shape always matches the manifest.
export async function runPersistedQuery<TVars, TData>(
  op: PersistedOperation,
  variables: TVars,
): Promise<{ data?: TData; errors?: Array<{ message: string }> }> {
  const res = await fetch(httpEndpoint, {
    method: 'POST',
    credentials: 'include',
    headers: authHeaders(),
    body: JSON.stringify({
      operationName: op.name,
      variables,
      extensions: {
        persistedQuery: { version: 1, sha256Hash: op.hash },
      },
    }),
  })
  if (!res.ok) {
    // Some upstream errors return an empty body — display "(no body)"
    // rather than the previous "graphql 500: " which left users
    // staring at a colon with nothing after it. Trim and JSON-pretty
    // any structured error so the message is actually readable.
    const raw = (await res.text()).trim()
    let body = raw
    if (raw === '') {
      body = '(empty body)'
    } else if (raw.startsWith('{')) {
      try {
        const parsed = JSON.parse(raw) as { error?: string; message?: string; errors?: Array<{ message: string }> }
        body = parsed.error || parsed.message ||
          (parsed.errors && parsed.errors.map((e) => e.message).join('; ')) ||
          raw
      } catch {
        // not valid JSON; fall through with raw
      }
    }
    throw new Error(`graphql ${res.status}: ${body}`)
  }
  return res.json()
}
