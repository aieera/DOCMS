# ADR 0090 — iPaaS integrations (Zapier / Make / n8n)

**Status:** Accepted (in-repo surface landed); vendor app builds pending.

**Date:** 2026-05-17

## Context

Blueprint §12.7 calls for "publishing Zapier + Make + n8n apps." Two
halves of work:

1. **In this repo**: stable HTTP endpoints those apps will call, plus
   admin UX for issuing the scoped API keys those apps authenticate
   with.
2. **External to this repo**: the actual app definitions, which live
   on each vendor's developer portal. Zapier's "Zapier app" is built
   in `zapier.com/developer/builder`. Make modules live on
   `make.com`'s Apps Developer panel. n8n nodes are npm packages.

This ADR covers (1) only. The playbook entry's "ADR 0081" reference is
stale — 0081 was already taken by litellm-routing; we use 0090.

## Decision

### Trigger endpoints

Three polling endpoints, one per resource that surfaces a useful event
for automation:

| Trigger | URL | Owning service |
|---|---|---|
| `document.created` | `GET /api/v1/integrations/triggers/documents` | document (:8182) |
| `signature.completed` | `GET /api/v1/integrations/triggers/signatures/completed` | signature (:8188) |
| `workflow.completed` | `GET /api/v1/integrations/triggers/workflows/completed` | workflow (:8186) |

All accept `?since=<RFC3339>&limit=<1-100>`. Default `since` is
"1 hour ago" so a Zap with no cursor doesn't replay the entire
history on first poll. Results are ordered by the relevant timestamp
ASC so Zapier's deduplicate-by-id pattern works.

### Authentication

Bearer API key (`Authorization: Bearer vdms_...`). Keys are issued
in `/admin/integrations/ipaas` and carry a `scopes` array.
`integrations:read` is required for the trigger endpoints; the
`integrations:*` shorthand grants any future `integrations:write`
scopes too.

Validation lives in `pkg/middleware/apikey.go` — the same SQL the
auth service uses (`SELECT FROM api_keys WHERE key_hash = sha256(token)`),
inlined into each service so a public-API call doesn't add a cross-
service gRPC hop. The api_keys table has globally unique `key_hash`
so the lookup needs no tenant predicate.

In dev, the app DB role has BYPASSRLS — the lookup just works. In
prod where the app role is NOBYPASSRLS, the policy on `api_keys`
needs a read-by-hash exception, OR services should delegate to an
auth-service gRPC call. This is a follow-up; flagged in
`pkg/middleware/apikey.go`'s doc comment.

### Gateway-signature whitelist

`/api/v1/integrations/triggers/` is added to
`pkg/middleware/gatewaysig.go`'s `publicExternalCallbackPrefixes`.
External iPaaS apps can't supply `X-Gateway-Signature` — the API key
is the trust anchor for these endpoints instead.

### Admin UI

`/admin/integrations/ipaas` provides:

- API key list (name, prefix, scopes, last-used, expires)
- "New API key" modal with scope multi-select + optional expiry
- One-time plaintext key reveal on creation
- Revoke action
- Copyable trigger-endpoint URLs
- Getting-started cards linking to Zapier / Make / n8n developer portals

### What is OUT of scope for this ADR

- **The Zapier app itself.** Built on zapier.com/developer/builder UI.
  Configure each trigger as a Polling URL pointing at our endpoints
  above. Use API Key authentication with header `Authorization`.
- **The Make module.** Same shape on make.com Apps Developer panel.
- **The n8n community node.** Lives in a separate repo as an npm
  package `n8n-nodes-vaultdms`.
- **Action endpoints.** Upload / start-workflow / search are existing
  endpoints in the document/workflow/search services. Zapier "actions"
  call those directly; no new code needed. The `integrations:write`
  scope exists to gate them when we wire scope-checking onto those
  endpoints (currently they require session auth).
- **Webhook subscriptions.** Polling is the v1 model. Webhook fan-out
  to customer URLs is better UX but adds significant complexity; the
  connector service already has a `webhooks` table that could be
  repurposed for this later.
- **Rate limiting on the trigger endpoints.** Same workstream as the
  rest of the rate-limit story (separate, not blocking).
- **Playwright tests against real vendor sandboxes.** Each vendor
  would need a pre-provisioned test account in CI.

## File map

| Path | Purpose |
|---|---|
| `pkg/middleware/apikey.go` | Bearer-token → ctx middleware with scope check |
| `pkg/middleware/gatewaysig.go` | Whitelist for `/api/v1/integrations/triggers/` |
| `services/document/internal/handler/integrations_triggers.go` | `documents` trigger |
| `services/signature/internal/handler/integrations_triggers.go` | `signatures/completed` trigger |
| `services/workflow/internal/handler/integrations_triggers.go` | `workflows/completed` trigger |
| `services/{document,signature,workflow}/cmd/server/main.go` | Route mount + APIKeyAuth wrap |
| `web/src/routes/_authenticated/admin/integrations/ipaas.tsx` | Admin page |
| `web/vite.config.ts` | Per-path proxy entries to the right backend service |

## Acceptance signal

End-to-end smoke from a real iPaaS app:

1. Admin issues a key with `integrations:read` scope.
2. iPaaS app (or curl) sets `Authorization: Bearer vdms_...` and polls
   `GET /api/v1/integrations/triggers/documents?since=2026-05-17T00:00:00Z`.
3. Response is a JSON array of documents created/updated in the
   window. Each has stable `id` + `created_at` / `updated_at`.
4. `last_used_at` on the key row increments on each poll.
5. Revoking the key flips subsequent polls to 401.
