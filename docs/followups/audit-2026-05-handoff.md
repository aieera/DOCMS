# Audit 2026-05 — backend handoff

Consolidated handoff for backend work that was logged across the
2026-05 frontend audit but deliberately not built in that PR.

**FE audit branch:** `chore/eslint-flat-config`.
**Audit outcome:** 23 of 24 named bugs closed in the FE; 1 left
open as backend track (M-7); 5 systemic patterns guarded against
recurrence with tests, helpers, and CI checks. The 2026-05-22
follow-up bug report added one more backend track (Bug 6) plus
one latent issue surfaced during recon (Kong route gap, track 7).

Each track below lists:
- The site (file + line numbers where known)
- Why it isn't in the FE PR
- Acceptance criteria
- The shape of the change

The intent is that a backend engineer can pick up any one track
without further conversation.

---

## Track 1 — Trash backend

**Origin:** audit H-2 + L-1. FE landed as `<ComingSoon/>` placeholder
in `web/src/routes/_authenticated/trash.tsx` (commit `4f61a6e`) until
the backend exists.

**Why not in the FE PR:** soft-delete is intentional in this codebase
(`services/document/internal/janitor/orphan_gc.go:31`: "Hard delete is
owned by the existing retention pipeline so cascading cleanup goes
through one code path"). Trash UI must not invent a hard-delete path
the rest of the system doesn't sanction. Three endpoints missing.

### Endpoints

```
GET /api/v1/trash?workspace_id=<optional>
```

List documents with `deleted_at IS NOT NULL` across workspaces the
caller can access.

- Permission: workspace `admin`/`owner` on the workspace, OR the
  document's original `created_by` (give the deleter their own
  trash). Cross-tenant access denied by RLS.
- Pagination: `?cursor=...&limit=...` matching the existing
  `pkg/database` cursor pattern.
- Response: `Page[Document]` with `deleted_at` populated, plus
  `deleted_by_user_id` (the user who triggered the soft-delete —
  requires either an `audit_events` join or extending the
  `documents` row with `deleted_by`).

```
POST /api/v1/documents/{id}/restore
```

Clear `deleted_at` (and `deleted_by_*` if added). 204 on success.

- Permission: same as the list above.
- Refuse with 409 if `legal_hold = true`.
- Refuse with 409 if a retention rule has elapsed
  (i.e. the document is past its allowed restoration window — read
  from the retention pipeline's source-of-truth).
- Emit `dms.document.restored.v1` via the existing outbox so
  search re-indexes and the audit log records the event.

**No purge endpoint.** The audit's first instinct was a `DELETE
/documents/{id}/purge` but soft-delete-only is the deliberate policy.
Retention owns hard-delete; the trash UI should NOT expose a way to
bypass that.

### Acceptance criteria

- `GET /api/v1/trash` returns soft-deleted documents the caller has
  permission to see; RLS prevents leakage across tenants.
- `POST /documents/{id}/restore` clears `deleted_at` and emits the
  outbox event; rejected with 409 on legal hold or expired retention.
- Existing integration tests for soft-delete still pass (no regression
  in the existing `services/document` test suite).
- New test: `services/document/internal/handler/trash_test.go` covers
  list + restore happy paths + the two refusal paths + an attempt to
  restore another user's deletion on a workspace the caller isn't
  admin on (must 404, not 403, to avoid existence leak).

### FE swap, after this lands

`web/src/routes/_authenticated/trash.tsx` becomes a real list +
restore flow. The `ComingSoon` panel currently there documents the
backend pre-conditions inline, so the swap is mechanical.

---

## Track 2 — Password-reset backend

**Origin:** audit M-6. FE landed as `<ComingSoon/>` placeholder in
`web/src/routes/_authenticated/forgot-password.tsx` (commit `07bb160`)
with copy that tells locked-out users to contact their admin (which
already works via the `/admin/users/invite` flow, `services/auth/
internal/handler/admin.go:119`).

**Why not in the FE PR:** auth-service router enumeration shows
zero password-reset routes — no `/forgot-password`, no
`/reset-password`, no `password_reset_tokens` table, no notification
template. Building it ad-hoc would either skip the enumeration-oracle
defence or skip the session-invalidation requirement. Needs its own
PR with the proper threat model.

### Endpoint A — request reset

```
POST /api/v1/auth/forgot-password
body: { email, tenant_slug }
response: always 200 with generic body
  { "message": "If an account is registered with this email, a reset link has been sent." }
```

**Must NOT differ by whether the email exists** — that's an
enumeration oracle. Same response shape, same latency
(introduce a small randomized delay floor if the lookup path is
materially faster than the no-op path).

Server-side, when the user exists AND is active:

1. Mint a high-entropy single-use token (256 bits, hex-encoded).
2. Hash (SHA-256) and insert into a new `password_reset_tokens`
   table:
   ```sql
   CREATE TABLE password_reset_tokens (
     tenant_id   uuid NOT NULL REFERENCES organizations(id),
     user_id     uuid NOT NULL,
     token_hash  text NOT NULL,
     created_at  timestamptz NOT NULL DEFAULT now(),
     expires_at  timestamptz NOT NULL,        -- now() + 30 min
     used_at     timestamptz,
     ip_address  inet,
     PRIMARY KEY (tenant_id, token_hash),
     FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id)
   );
   CREATE INDEX idx_reset_tokens_lookup
     ON password_reset_tokens (token_hash) WHERE used_at IS NULL;
   ```
   Force RLS on `tenant_id` per the existing pattern. The partial
   index keeps lookups cheap as the table grows.
3. Emit `dms.auth.password_reset_requested.v1` via the existing
   outbox so the notification service can send a localized email
   (template needs en + ar per ADR 0106 / 0107) with the link
   `<public_url>/reset-password?token=<plaintext-token>`.
4. Rate-limit per-IP AND per-email so the endpoint isn't an
   enumeration oracle / spam vector. The
   `pkg/middleware/ratelimit.go` two-key shape supports this.

### Endpoint B — apply reset

```
POST /api/v1/auth/reset-password
body: { token, new_password }
```

Server-side:

1. Look up by `sha256(token)`; reject if missing, used, expired, OR
   older than the user's last password change (defense against
   stolen mail-archive replays).
2. Validate `new_password` against the existing
   `services/auth/internal/service.validatePassword` (12-128 chars,
   upper/lower/digit/special). Reuse — don't fork the policy.
3. Update `users.password_hash` in a transaction; mark the token
   `used_at = now()`.
4. **Invalidate every existing session for that user** — delete all
   rows in `sessions` for that `user_id` — so a stolen-cookie attack
   doesn't survive the reset.
5. Emit `dms.auth.password_changed.v1`.
6. Return 204 No Content.

### Acceptance criteria

- `forgot-password` returns identical 200 + identical timing for
  registered vs unregistered emails (table test pins this).
- A `reset-password` call invalidates every session for the user
  (assert `SELECT count(*) FROM sessions WHERE user_id = ?` is 0
  post-reset).
- Token replay rejected (use a token twice → second call gets 410).
- A token issued before the user changed their password is rejected
  even before expiry (assert `created_at < users.password_changed_at`).
- Notification email is sent through the existing notification
  service; localized via the same i18next-equivalent backend setup as
  other transactional mails.

### FE swap, after this lands

`web/src/routes/_authenticated/forgot-password.tsx` → real form
posting to endpoint A; the response is the generic success copy
unconditionally. Add `web/src/routes/_authenticated/reset-password.tsx`
for endpoint B (consumes `?token=` query param).

---

## Track 3 — `X-Tenant-ID` legacy header migration

**Origin:** audit M-7. **STAYS OPEN as a backend track per explicit
user direction.** FE `client.ts` still dual-writes `X-Auth-Tenant-ID`
+ `X-Tenant-ID`; the deletion-pending comment block on
`web/src/api/client.ts:136` (added in commit `8adfd3b`) enumerates
every reader.

**Why not in the FE PR:** removing the dual-write today would
400-bomb the entire AI surface (intelligence service's 21 routes)
plus risk 401s on any Go service whose mount chain doesn't pre-populate
the auth context before `pkg/middleware.TenantHTTP`. Mitigation:
`RequireGatewaySignature` boundary already gates the legacy header so
it's "trusted via the signed gateway", not "trusted unconditionally".

### Inventory of readers still on the legacy header

Each of these reads `X-Tenant-ID` with no fallback to
`X-Auth-Tenant-ID`:

| Reader | File:line | Notes |
|---|---|---|
| `pkg/middleware/tenant.go:83` | `TenantHTTP` canonical middleware | Used by most Go services; has a session-cookie context fallback when `SessionAuth` precedes it in the chain, so some services are already safe — but it's per-service mount order. Audit before declaring any one safe. |
| `services/intelligence/app/api/routes.py` (21 routes) | FastAPI `Header(None, alias="X-Tenant-ID")` declarations | No fallback. Removal would 400 every call: `/ask`, `/qa`(+`/sync`,`/history`), `/rag/query`(+`/feedback`), `/summarize`, `/translate`, `/translations/*`, `/language/*`, `/redact/{detect,apply}`, `/anomaly/run`, `/llm/completions`, `/workspaces/{id}/ai-settings` GET+PUT. |
| `services/document/internal/handler/storage_proxy.go:316` | Direct read via `middleware.TenantHeader` | Upload-initiate path; sets gRPC metadata for the storage service. |
| `pkg/gateway/ratelimit.go:49` | Token-bucket keying | Silent degradation if removed (all anonymous traffic in one bucket) rather than outage. |
| `pkg/gateway/cors.go:33` | CORS allowlist check | |
| `pkg/metrics/metrics.go:130` | Prometheus tenant label | Cosmetic mis-tagging if removed. |

### Safe removal sequence (do **not** skip steps)

a. **Intelligence (FastAPI)** — extract one dependency function that
   reads `X-Auth-Tenant-ID` first, falls back to `X-Tenant-ID`,
   raises `HTTPException(400)` if both missing. Swap all 21 route
   declarations to `Depends(get_tenant_id)`. About 20 lines plus the
   per-handler line edits.

b. **`pkg/middleware/tenant.go`** — change `TenantHTTP` so it reads
   `X-Auth-Tenant-ID` first, then falls back to `X-Tenant-ID`, then
   to the cookie-derived `auth.GetTenantID(ctx)`. One change covers
   every Go consumer of `middleware.TenantHeader`. The constant
   itself stays at `"X-Tenant-ID"` for now to avoid grep-fan-out.

c. **`services/document/internal/handler/storage_proxy.go`** — same
   dual-read pattern.

d. **`pkg/gateway/{cors,ratelimit}.go` + `pkg/metrics/metrics.go`** —
   same dual-read pattern. Three single-line edits.

e. **Deploy backend** — verify every prod replica is on the new
   code (`docker compose ps`, `kubectl rollout status`, etc.). No
   canary still reading the legacy name.

f. **ONE release later — delete the dual-write from
   `web/src/api/client.ts`.** Delete the whole comment block on
   lines 136-184 AND the `config.headers['X-Tenant-ID'] = tenantId`
   line. Confirm no 400s for `/ask`, `/qa`, `/summarize`, `/translate`,
   `/redact` in the first hour post-deploy.

### Acceptance criteria

- All six readers above accept both headers; tests pin the
  dual-read on at least the canonical `TenantHTTP` middleware
  (`pkg/middleware/tenant_test.go`).
- After step (f), the audit FE's `grep -rE "X-Tenant-ID"
  web/src/` shows only comments + non-auth uses (URL templating
  in `admin/integrations/index.tsx`, query param parsing in
  `accept-invite.tsx`). Both are explicitly NOT the H-1 trap and
  were verified clean at the time of the FE audit.

---

## Track 4 — Pattern-1 consolidation (un-migrated mutations)

**Origin:** Wave 5 pattern 1 of the audit. Commit `d66f245` migrated
**26 mutations** across 12 files to `useAppMutation` (the wrapper
hook in `web/src/hooks/useAppMutation.ts` that injects a default
`onError → toast` unless overridden). Option B scope; the remaining
**~209 `useMutation` call sites** in admin route files + component-
local mutations + intelligence/admin sub-trees were deliberately not
swept.

**Why not in the FE PR:** Option A (full ~70-file sweep) would have
hidden regressions in the audit diff. Option B (8 high-traffic hooks
+ the 4 explicitly-named parked sites) covers the majority of
mutation call volume with a reviewable diff. The leftover sites are
each a one-line swap; collectively they're a pure consolidation pass.

**Not blocking — there are no broken mutations.** Each unmigrated
site that lacks `onError` already had that gap before the audit and
still has it; the audit didn't make it worse, just didn't fix it
on a sweep that would have been hard to review.

### Scope

Repo-wide grep:

```
$ grep -rln "useMutation\b" web/src/ | grep -v __tests__ | wc -l
70   # number of files
$ grep -rEn "useMutation\b" web/src/ | grep -v __tests__ | wc -l
235  # number of call sites
```

26 migrated, ~209 remain.

### Migration recipe (per file)

For each `useMutation(` call site:

1. Replace `useMutation` with `useAppMutation` (import from
   `@/hooks/useAppMutation`).
2. If the site has its own `onError` handler that's anything more
   substantial than a single `toast.error('...failed')`, leave the
   handler intact — `useAppMutation`'s "caller wins" branch covers it.
3. If the site has no `onError`, optionally add a
   `defaultErrorMessage: 'Could not <action>'` for nicer copy than
   the wrapper's "Something went wrong" final-fallback.
4. Verify nothing about `onSuccess`, `onMutate`, `onSettled`,
   `mutationKey`, or `mutationFn` changes — the wrapper preserves
   all of them.

### Acceptance criteria

- After the sweep, `grep -rln "useMutation\b" web/src/` returns only
  test files (and the wrapper hook itself).
- The 8 wrapper invariant tests in
  `web/src/hooks/__tests__/useAppMutation.test.tsx` still pass.
- No new tests required per-site; the wrapper's invariants cover
  every consumer.

### High-priority files (start here)

The biggest remaining concentrations:

- `web/src/routes/_authenticated/admin/` (~50 mutation sites across
  ~20 admin route files)
- `web/src/components/admin/{ESignCredentialsModal,GoogleWorkspaceModal,
   M365ConnectorModal,SMTPCredentialsModal,TwilioCredentialsModal,
   UserTable}.tsx` (~10 sites)
- `web/src/components/intelligence/*` (~10 sites — currently AI
  surface, naturally over-represented in error-handling debt)

---

## Track 5 — Smaller follow-ups

Each is a single ticket-sized item.

| ID | Site | Recommended fix |
|---|---|---|
| 5a | `web/src/api/security.ts:41` — duplicate `listSessions` | A SECOND `listSessions` helper points at the same `/auth/sessions` endpoint as `web/src/api/auth.ts:60` (already migrated through `unwrapList<SessionRow>` in Turn 1). The security.ts version still does `data.sessions ?? []` silent-fail. Consolidate: delete `api/security.ts:listSessions`, retarget `hooks/useSecurity.ts:33` at the auth.ts export. |
| 5b | Other `getWorkspaces`/`getFolders` consumers | `FolderTree.tsx`, `WorkspaceSelector.tsx`, `useFolders.ts`, `useWorkspaces.ts`, `routing-rules.tsx`, `ask.tsx`, `useUpload.ts` all consume the helpers but lack an `isError` UI branch. The helpers now throw `UnknownListShapeError` (Wave 5 pattern 3); these consumers silently degrade to "no items" on a malformed response. Add isError UI per the `VersionHistory.tsx` template from Turn 1. |
| 5c | `useAppMutation` back-off invariant test | `web/src/api/client.ts` exports `__resetHydrationBackoffForTests` (Wave 4 / M-1). Write a fake-timer test in `web/src/api/__tests__/client.test.ts` proving (1) at most one `/auth/me` round-trip fires inside `HYDRATION_BACKOFF_MS`, (2) one more after the window elapses. |
| 5d | `useAuth.useLogin` re-introduction guard | The orphan hook was deleted in Turn 5 because it carried the `tenant_id ?? ''` H-1 trap. Add a lint rule (or just a `git grep -F "tenant_id ?? ''"` line in CI alongside the mojibake guard) so a future copy-paste of the bad pattern fails the build. |
| 5e | Button destructive-variant test | `web/src/components/ui/__tests__/Button.test.tsx:35` asserts `toHaveClass('bg-red-500')` but the destructive variant uses different Tailwind classes. Pre-existing genesis-era debt (commit `0f91b7d`), not introduced by the audit; surfaces as the single red in the test suite. Fix the assertion to match the current `buttonVariants` output. |
| 5f | L-5 auth-form Zod migration | `register.tsx`, `login.tsx`, `accept-invite.tsx` use raw `useState` + manual validation. Standardize on `useForm + zodResolver` using `web/src/components/ui/form.tsx` (the shadcn primitive already in use). Start with `register.tsx` as the template. Out of audit scope; usability cleanup. |

---

## Track 6 — Search highlight precision (Bug 6 from 2026-05-22 web bug report)

**Origin:** 2026-05-22 user-reported bug: searching `"contract
termination"` highlights the word `"Contact"` in result snippets;
single-word `"invoice"` highlights `"invoice"` correctly. Triage
showed the FE does zero highlight processing — it renders
backend-emitted `<mark>` HTML via `dangerouslySetInnerHTML` in
`web/src/routes/_authenticated/search.tsx:365-385`.

**Why not in the FE PR:** the wrong-token comes from the OpenSearch
query, not from the FE. Any FE-side correction would have to
re-implement the analyzer client-side without access to the index's
synonyms, stemmer, or fuzziness expansions — strictly worse than the
current state.

### Site

`services/search/internal/opensearch/query.go:80` — the main
multi_match clause:

```go
"multi_match": map[string]any{
    "query":     req.Query,
    "fields":    []string{"title^3", "title.keyword^5", "title.autocomplete^2", "tags^2", "description^1.5", "content^1", ...},
    "type":      "best_fields",
    "fuzziness": "AUTO",   // ← admits edit-distance 1-2 near-misses
    "lenient":   true,
},
```

### Cause

`AUTO` fuzziness allows Damerau-Levenshtein edit-distance **1 for
3-5-char terms** and **2 for 6+-char terms**. `"contract"` is
8 chars, so the query admits any term within 2 edits — and
`"contact"` is exactly 1 edit away (delete the `r`). OpenSearch
matches `"contact"` as a fuzzy hit and the unified highlighter
dutifully `<mark>`s it because that IS what the query matched. The
behavior is internally consistent; only the UX suffers.

Single-word `"invoice"` works because no real English word is within
edit-distance 2 of "invoice".

### Recommended fix (Option A — minimal blast radius)

Add a `highlight_query` to the OpenSearch request body in `query.go`
around line 105-115 that re-runs the user's query **without**
fuzziness. The unified highlighter then marks only tokens that match
exactly, while the outer query keeps AUTO fuzziness so recall is
preserved (typo tolerance: `"contarct"` still finds `"contract"`).

Sketch:

```go
if req.Highlight {
    body["highlight"] = map[string]any{
        "fields": map[string]any{
            "title":   map[string]any{"number_of_fragments": 1, "fragment_size": 200},
            "content": map[string]any{"number_of_fragments": 3, "fragment_size": 150},
        },
        "pre_tags":  []string{"<mark>"},
        "post_tags": []string{"</mark>"},
        // Highlight only tokens that match without fuzziness.
        // Outer query (above) keeps AUTO fuzziness for recall.
        "highlight_query": map[string]any{
            "multi_match": map[string]any{
                "query":  req.Query,
                "fields": []string{"title", "content", "tags", "description"},
                "type":   "best_fields",
                // No fuzziness here.
            },
        },
    }
}
```

Alternatives rejected:

- **Option B — `prefix_length: 3` on the outer fuzziness**: changes
  search RELEVANCE (some genuine typos at the leading 3 chars no
  longer match). Wider blast radius than necessary.
- **Option C — lower default fuzziness to `1`**: same problem,
  larger. Changes recall, not just highlights.

### Acceptance test

New file `services/search/internal/opensearch/highlight_test.go`
exercising the search service against a fixture OpenSearch index
populated with both `"contract termination clause"` and `"contact
us page"` documents:

- Query `"contract termination"` → response `highlights` wraps only
  `<mark>contract</mark>` / `<mark>termination</mark>`, never
  `<mark>Contact</mark>` or `<mark>contact</mark>`.
- Query `"invoice"` → wraps `<mark>invoice</mark>`.
- Query `"contarct"` (typo) → still matches the contract doc via the
  outer query's AUTO fuzziness, but the document title/snippet
  renders unhighlighted (no exact-match token to wrap). This is the
  correct behavior — the highlight tells the user "this is what we
  matched on your literal terms", and a pure typo-recovery hit has
  no literal token to highlight.

### Acceptance criteria

- The three test cases above pass against a real fixture index.
- No regression in existing search relevance tests (whatever else
  lives in `services/search/internal/opensearch/*_test.go`).

---

## Track 7 — Kong route gap for `/api/v1/intelligence/*`

**Origin:** surfaced during the 2026-05-22 Bug-1-5 root-cause recon.
Confirmed via `grep -E "intelligence" deploy/gateway/kong.yaml`:
**no matches**. AI endpoints work today via Vite host-mode proxy
(`web/vite.config.ts:133` routes `/api/v1/intelligence` to
`http://localhost:8194`), but the moment the FE flips to
`VITE_PROXY_MODE=gateway`, every intelligence call 404s at Kong
because Kong has no upstream for that path.

**Why not in the FE PR:** the FE is correct; the gateway config is
missing the route. This is gateway / deploy work, not a code change.

### Site

`deploy/gateway/kong.yaml` — the `services:` block has entries for
auth, policy, document, storage, search, audit, workflow,
notification, signature, billing, connector, graphql-gateway,
mcp-server, but **no entry for intelligence**.

### Fix

Add an `intelligence` service entry mirroring the existing service
shape (look at `audit` or `notification` for the template). Upstream
URL is `http://intelligence:8081` per the
`docker-compose.yml` env (intelligence's `SEDOC_HTTP_PORT=8080`
inside the container — but check whether Kong's other services use
`:8080` or `:8081`; the rest of the kong.yaml currently routes to
`:8081` for every Go service via a convention that may or may not
match Python services).

Routes to add (all 21 AI routes the intelligence service exposes —
each currently lives in `services/intelligence/app/api/routes.py`):

```yaml
- { name: intel-ask,             paths: [/api/v1/intelligence/ask],           strip_path: false }
- { name: intel-qa,              paths: [/api/v1/intelligence/qa],            strip_path: false }
- { name: intel-rag,             paths: [/api/v1/intelligence/rag],           strip_path: false }
- { name: intel-summarize,       paths: [/api/v1/intelligence/summarize],     strip_path: false }
- { name: intel-translate,       paths: [/api/v1/intelligence/translate],     strip_path: false }
- { name: intel-translations,    paths: [/api/v1/intelligence/translations],  strip_path: false }
- { name: intel-language,        paths: [/api/v1/intelligence/language],      strip_path: false }
- { name: intel-redact,          paths: [/api/v1/intelligence/redact],        strip_path: false }
- { name: intel-anomaly,         paths: [/api/v1/intelligence/anomaly],       strip_path: false }
- { name: intel-llm,             paths: [/api/v1/intelligence/llm],           strip_path: false }
- { name: intel-workspaces-ai,   paths: [/api/v1/intelligence/workspaces],    strip_path: false }
```

Or a single catch-all:

```yaml
- { name: intelligence-all, paths: [/api/v1/intelligence], strip_path: false }
```

Per-route is more aligned with the existing convention; the catch-all
is shorter and easier to maintain. Either works.

### Acceptance criteria

- `curl http://kong:8080/api/v1/intelligence/qa -X POST` returns the
  intelligence service's 4xx/2xx (validation or success), NOT a Kong
  404.
- The 21 intelligence routes are reachable via Kong with the same
  status codes they return via Vite host-mode.
- FE switched to `VITE_PROXY_MODE=gateway` works against the AI
  surface end-to-end.

### Pairs naturally with Track 3

Both touch the gateway-trust path. Doing them in the same PR keeps
the gateway-config surgery in one review.

---

## What's NOT in the handoff

Two pieces of state worth recording even though they aren't
actionable items:

### Python image build defensive line (in this commit, not a track)

The 2026-05-22 cycle's `docker compose build` for the four Python
services (intelligence, intelligence-worker, intelligence-worker-misc,
preview-worker) hit `metadata-generation-failed` on at least one
build attempt. Root cause for preview-worker was confirmed:
`PyMuPDF==1.23.0` is a source-distribution-only pin that fails to
compile MuPDF from C under `python:3.12-slim`. Two changes shipped
with this audit handoff to make the builds deterministic going
forward:

- **`services/intelligence/Dockerfile:16` + `services/preview/Dockerfile:19`**
  added `RUN pip install --no-cache-dir --upgrade pip setuptools wheel`
  before the requirements install. Defensive against
  PEP-517-related metadata generation failures on old pip /
  setuptools combos that `python:3.12-slim` still ships.
- **`services/preview/requirements.txt:6`** bumped
  `PyMuPDF==1.23.0` → `PyMuPDF==1.24.10`. The 1.24.x line ships
  prebuilt cp312 wheels (no C compile). Matches the
  `services/intelligence/requirements.txt` pin so the repo runs one
  PyMuPDF version. API surface preview-worker uses (`fitz.open`,
  `doc.needs_pass`, `len(doc)`, page iteration,
  `page.get_text("text")`, `doc.close()`) is stable since pre-1.18 —
  not 1.23-specific.

Tested: all 4 Python containers came up healthy after the changes,
all 28 services running, intelligence `/healthz` returns 200,
`/api/v1/intelligence/qa` returns 422 (validation) instead of 500.

---

## Track 8 — Share → in-app notification not delivered

**Origin:** Phase 9 verification of "notify-on-share". The FE
`ShareDialog` flow calls the share endpoints, which DO persist
share rows, but the recipient never sees an in-app notification.
Two emitter sites diverge from the notification consumer's
contract:

### Gap 8a — Classic share-link event subject doesn't reach the consumer

`services/document/internal/service/sharing_tags.go:105` emits
`dms.sharelink.created.v1`:

```go
evt, err := model.NewOutboxEvent(tenantID, "dms.sharelink.created.v1", "share_link", link.ID,
    model.ShareLinkCreatedPayload{
        LinkID:     link.ID.String(),
        DocumentID: doc.ID.String(),
        CreatedBy:  userID.String(),
        ExpiresAt:  expiresAt.UTC().Format(time.RFC3339),
    })
return s.repos.Outbox.Insert(ctx, tx, evt)
```

The notification service consumer at
`services/notification/internal/service/service.go:217` subscribes
to `dms.notify.>` only:

```go
js.Subscribe("dms.notify.>", func(msg *nats.Msg) { ... })
```

So `dms.sharelink.created.v1` is published, lands in JetStream,
and is never delivered to a `notifications` row. No badge appears
in the FE topbar; nothing renders in `/notifications`.

### Gap 8b — ZT share emits no event at all

`services/document/internal/handler/zt_share_handler.go` creates
ZT share tokens but never calls `outbox.Insert`. There is no
event for the notification service to consume, no matter what
subject it subscribes to.

### Fix (backend track)

Pick one of:

**Option A — bridge in the notification consumer.** Subscribe
to `dms.sharelink.created.v1` (and a new `dms.ztshare.created.v1`
once 8b is fixed) in `services/notification/internal/service/service.go`.
Resolve the recipient principal(s) from the share row, then
`Deliver()` a notification with `type='document.shared'`. Subject
mapping stays out of share business logic.

**Option B — emit a `dms.notify.document_shared.v1` directly.**
Add the second event publish in both share handlers so the
notification consumer's existing wildcard catches it. Cheaper but
couples share code to the notification taxonomy.

For ZT share (Gap 8b), also add `outbox.Insert` for a new
`dms.ztshare.created.v1` (or whichever subject the bridge wires
up) so the recipient is reachable.

### Acceptance

- Creating a share-link or ZT share creates a row in
  `notifications` for each named recipient.
- Recipient sees a topbar badge (already wired to
  `useNotifications`) and a row in `/notifications`.
- Existing notification consumer tests (`services/notification/...`)
  still pass; add one regression for the new subject.

### FE handling

No FE change required. The notifications inbox at
`web/src/routes/_authenticated/notifications.tsx` already renders
arbitrary notification types via the generic `title`/`body`/`type`
shape; once the backend delivers, the rows appear automatically.
Deep-link target (the document URL) should be set on the
notification's `resource_id`/`resource_type` so the existing
"click row → navigate" handler routes the user correctly.

---

### Audit-branch state

| Metric | Value |
|---|---|
| FE audit branch | `chore/eslint-flat-config` |
| Audit commits | 14 (from `79cd52c` through Turn 5's `03ec5ae`, plus the Python image + handoff commits this turn) |
| Lint status | exit 0 (eslint + no-physical-tw + no-mojibake) |
| tsc status | exit 0 |
| Build status | exit 0 (`npm run build` produces `dist/`) |
| Test status | 92 of 93 passing. The one red is the pre-existing `Button > applies destructive variant class` test from genesis commit `0f91b7d`, unrelated to the audit, captured as Track 5e. |
| New `any` introduced by audit | 0 |
| New `@ts-ignore` introduced by audit | 0 |
| New unjustified `eslint-disable` | 0 |
| Total `any` count repo-wide | 286 warnings (down from 304 pre-audit) |
| Mojibake count | 0; CI guard active (`npm run lint:utf8`) |
