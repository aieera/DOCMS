// unwrapList — single normalization point for the recurring
// dual-shape API response pattern the Wave 3 audit flagged on
// getVersions (H-3), listSessions (L-3), getWorkspaces (M-2), and
// getFolders (same class, bonus consolidation).
//
// Background: grpc-gateway translates `repeated foo` proto fields
// into a top-level `{ foos: [...] }` JSON envelope, but the typed
// FE callers historically expected a bare `T[]`. The old per-helper
// pattern was:
//
//     return Array.isArray(data) ? data : (data.foos ?? [])
//
// which silently returned `[]` on a malformed response — a payload
// like `{ "error": "..." }` would render as "no items" instead of
// surfacing the actual failure. Audit Wave 5 pattern 3: throw on
// unknown shapes so react-query routes the failure to its `isError`
// branch and the consuming component can render an error state.
//
// Throw rather than return-empty for malformed shapes because:
//   - react-query's queryFn is the right place to fail. A throw
//     here becomes `isError: true` on the consuming hook; an empty
//     array becomes `isError: false, data: []` — indistinguishable
//     from a healthy zero-item response.
//   - The dual-shape contract is supposed to be temporary (legacy
//     bare array + current `{key:[]}`). A response that's NEITHER
//     is a bug somewhere — backend regression, proxy mangled body,
//     auth interceptor returning HTML — and should not be papered
//     over.

export class UnknownListShapeError extends Error {
  constructor(public readonly key: string, public readonly received: unknown) {
    super(
      `unwrapList: expected an array or an object with a '${key}' array property, ` +
        `got ${describeShape(received)}`,
    )
    this.name = 'UnknownListShapeError'
  }
}

// describeShape — used by the error message so a maintainer reading
// the toast or console output sees what arrived instead of the bare
// "got [object Object]".
function describeShape(v: unknown): string {
  if (v === null) return 'null'
  if (Array.isArray(v)) return 'array' // shouldn't fire (caller already checked) but keep for safety
  const t = typeof v
  if (t !== 'object') return t
  const keys = Object.keys(v as object).slice(0, 5)
  return `object with keys [${keys.join(', ')}${keys.length === 5 ? ', …' : ''}]`
}

/**
 * unwrapList — normalize a dual-shape JSON list response.
 *
 * Returns the array on either of the supported shapes:
 *   - `T[]`                       (legacy bare array)
 *   - `{ [key]: T[] }`            (grpc-gateway-wrapped envelope)
 *
 * Throws `UnknownListShapeError` on anything else, including:
 *   - `null` / `undefined`
 *   - primitive (string, number, boolean)
 *   - object without `[key]` or with a `[key]` that's not an array
 *
 * Intended as the queryFn body for react-query hooks; the throw
 * routes failures to the hook's `isError` state for the consuming
 * component to render appropriately.
 */
export function unwrapList<T>(data: unknown, key: string): T[] {
  if (Array.isArray(data)) return data as T[]
  if (data && typeof data === 'object') {
    const wrapped = (data as Record<string, unknown>)[key]
    if (Array.isArray(wrapped)) return wrapped as T[]
  }
  throw new UnknownListShapeError(key, data)
}
