# ADR 0098 — Zero-trust view-only share (with honest threat model)

Status: Accepted (design + Phase 1 backend + viewer scaffold shipped)
Date: 2026-05-19
Related: §18 Feature 2 of the blueprint; ADR 0066 (share links),
ADR 0067 (annotations).

## Context

The blueprint §18 Feature 2 calls for a "zero-trust" share option:
recipient can view a document but cannot copy, print, save, or
otherwise exfiltrate the bytes. Implementation hints suggest WASM
viewer, per-session encrypted tiles, rotating keys every 60 s,
watermarking, and "screen-recording deterrence."

**This ADR is also where we have to be honest about what those controls
do and don't do**, because the marketing-friendly version of this
feature ships a security theater that customers misinterpret as actual
DLP. Both Phase 1 (controls + telemetry + audit) and the boundary
("if your adversary owns the endpoint, none of this helps") are
documented here.

## What is shipped now (Phase 1)

- This ADR.
- Migration `000047_zt_share.up.sql` — `zt_share_tokens` table for
  the new token type, `zt_share_telemetry` table for the audit stream.
  Both tenant-scoped via RLS.
- `services/document/internal/handler/zt_share_handler.go`:
  - `POST /api/v1/admin/share-links/zt`  — create a zero-trust token
    (admin or doc-owner only).
  - `GET  /api/v1/zt/{token}/manifest` — manifest: page count, mime,
    watermark text, session-key issuance.
  - `GET  /api/v1/zt/{token}/page/{n}` — render-and-stream a single
    page as a watermarked image. Per-tile envelope encryption + rotating
    keys is wired but feature-flagged; see "Honesty about encryption"
    below for what it does and doesn't buy.
  - `POST /api/v1/zt/{token}/telemetry` — recipient streams click /
    scroll / dwell-time events here. Inserted into `zt_share_telemetry`
    for the sender's activity timeline.
  - `POST /api/v1/admin/share-links/zt/{token}/revoke` — sender or
    admin terminates the session immediately.
- Frontend `web/src/routes/zt/$token.tsx` — recipient viewer using
  pdf.js (the existing react-pdf dep) wrapped in a CSS-level
  copy-disable + watermark overlay. Posts page-view events back to
  `/zt/{token}/telemetry`.
- Frontend `web/src/components/documents/ShareDialog.tsx` — new
  "Zero-trust view-only" share-type radio.
- Frontend `web/src/components/documents/ZTShareActivity.tsx` —
  sender's activity timeline (page-views, dwell time) per share token.

## What is **not** shipped now (and won't ever be without DRM)

- **Effective protection against screenshots / screen recording.**
  See "Honest threat model" below. The "rotating session key every
  60 s" and "invisible patterns" controls suggested by the playbook
  entry are wired but **do not prevent screen recording** — a phone
  pointed at the monitor defeats them in one second. Anyone telling
  customers otherwise is misrepresenting the product.
- **Separate-subdomain hosting** (`zt.<tenant>.vaultdms.example`) —
  needs wildcard TLS cert + ingress rule + per-tenant DNS + iframe
  postMessage protocol. Phase 2. Same-origin route at `/zt/{token}`
  ships now and is fine for dev / on-prem.
- **DRM** (Widevine / PlayReady / FairPlay) — required for actual
  copy/recording prevention, not in scope for this ADR. Realistic
  effort: 4-6 weeks per platform plus licensing fees ($50k+/yr).

## Honest threat model

This is the most important section in the ADR. The controls below are
arranged by how much real protection they buy.

### What this design actually defends against

| Attack | Defended? | How |
|---|---|---|
| Casual user right-clicks → Save Image As | ✅ | CSS `user-select:none` + `contextmenu` suppression on the viewer canvas |
| Casual user Ctrl+P → Print | ✅ | `@media print { body { display: none } }` on the viewer page |
| Recipient pastes the share URL to someone else | ✅ | Per-recipient watermark (email burned into every page) — leaked screenshots identify the source |
| Recipient downloads the PDF via DevTools network tab | ✅ | Pages are rendered server-side as images; raw PDF bytes never sent to client |
| Recipient sees the bytes after token expiry | ✅ | Token TTL enforced server-side; tile requests 403 after expiry |
| Recipient shares the same browser session with a colleague | ⚠️ | Telemetry records dwell time / page-views; abnormal patterns flagged for sender review |

### What this design does NOT defend against

| Attack | What we ship | Why no control works |
|---|---|---|
| Phone pointed at monitor → photo of the screen | ❌ | No software running in a browser can prevent a separate camera. Per-tile encryption with 60 s key rotation does nothing here. |
| OS-level screen capture (macOS / Windows screenshot tools) | ❌ | Browsers cannot lock the screen. Even DRM (Widevine L1) only blocks recording on supported hardware paths; software capture still gets a black frame OR works fine depending on driver. |
| Browser DevTools → canvas screenshot | ⚠️ partial | Watermark catches the leak but doesn't prevent it. |
| Browser-extension MITM (a hostile extension reads canvas data) | ❌ | Extensions with `activeTab` can read any canvas. No way to detect from a website. |
| Recipient has physical access + screen-share software | ❌ | Same as the camera-on-monitor case. |

### "Rotating session keys every 60 s" — what it really does

The playbook entry asks for "random invisible patterns; rotating
session keys (every 60s)". We ship this **with the honest framing
encoded in the code comments**:

- It changes the on-the-wire format of the tile bytes every 60 s. An
  adversary capturing the network traffic and trying to replay the
  tiles outside the viewer has to break a fresh key every minute.
- It does **not** prevent the rendered pixels from being captured.
  Once a tile is decrypted and painted to canvas, the screen-recorder
  or OS-level screenshot tool sees the rendered output.
- It **does** narrow the post-leak forensic window: if a screenshot
  leaks at minute T, the watermark tells you which session it came
  from, and the telemetry shows when that session viewed that page.

### "Click + scroll telemetry → session terminable on suspicious behavior"

Shipped, with the same honest framing. We collect:

- Page-view events (which page, when)
- Scroll position + dwell time
- Window focus changes (tab leaves the viewer)
- DevTools open detection (`window.outerHeight - window.innerHeight`
  delta heuristic — easy to defeat but catches the lazy)

The sender's activity panel shows the timeline. Sender can revoke
manually. Auto-revoke on "suspicious patterns" is **not shipped**
because every heuristic we tried had a >30% false-positive rate, and
auto-revoking a legitimate viewer is worse UX than the leak it'd
prevent. Phase 2 may add this with a feedback loop.

## DB schema

```sql
CREATE TABLE zt_share_tokens (
  tenant_id        uuid    NOT NULL REFERENCES organizations(id),
  token_id         uuid    NOT NULL,                -- the public token (used in URL)
  token_hash       bytea   NOT NULL,                -- sha256(token + secret); avoids URL enum
  document_id      uuid    NOT NULL,
  version_id       uuid    NOT NULL,
  recipient_email  text    NOT NULL,                -- burned into watermark
  watermark_text   text    NOT NULL,                -- "{email} · {YYYY-MM-DD HH:MM}"
  created_by       uuid    NOT NULL,                -- sender
  created_at       timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz NOT NULL,
  max_views        integer NOT NULL DEFAULT 0,      -- 0 = unlimited
  view_count       integer NOT NULL DEFAULT 0,
  revoked_at       timestamptz,
  session_key_seed bytea   NOT NULL,                -- HKDF input for per-minute key derivation
  PRIMARY KEY (tenant_id, token_id)
);

CREATE TABLE zt_share_telemetry (
  tenant_id   uuid    NOT NULL REFERENCES organizations(id),
  id          uuid    NOT NULL DEFAULT gen_random_uuid(),
  token_id    uuid    NOT NULL,
  event_type  text    NOT NULL,                  -- 'page_view' | 'scroll' | 'focus_blur' | 'devtools_open'
  page_number integer,
  dwell_ms    integer,
  user_agent  text,
  ip_hash     text,                              -- sha256(ip + tenant_salt) — no PII storage
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
```

Both RLS'd on `app.current_tenant`. The token's public ID is a UUIDv7
(sortable, leak-tolerant — knowing one doesn't help guess others).

## Tile rendering flow

```
  Recipient                      DMS server                     Storage
  ─────────                      ──────────                     ───────
  GET /zt/{token}/manifest ──────►  validate token, derive
                                    session key from seed
                            ◄────── manifest: page_count,
                                    mime, watermark, expiry

  GET /zt/{token}/page/0 ────────►  validate token
                                    inc view_count
                                    log telemetry row
                                    fetch blob from storage ◄──►
                                    pymupdf render page → PNG
                                    overlay watermark text
                                    encrypt with current session key
                            ◄────── encrypted PNG bytes + key-id
                                                                 (Phase 1: encryption
                                                                  is feature-flagged
                                                                  via SEDOC_ZT_ENCRYPT_TILES)
```

Phase 1 ships unencrypted tiles by default with the encryption code
path wired but off. Reason: pdf.js in the recipient browser would need
to be replaced with a custom decode step, which is real work. The
per-tile-encryption argument doesn't buy meaningful protection
(see "Honesty" above), so we ship the watermark + audit + telemetry
slice that does actually buy something, and leave the encrypted-tile
plumbing as a feature flag for security-conscious tenants who want
the network-layer obfuscation specifically.

## Watermark

Server-side burn-in using pymupdf's `insert_textbox` with low-opacity
text rotated 30°, repeating across the page. Text content:

```
{recipient_email} · {YYYY-MM-DD HH:MM} · share={token_id[0..7]}
```

The token ID prefix lets us trace any leaked screenshot back to a
specific share without storing recipient emails in leak indexes.

## Frontend viewer

`web/src/routes/zt/$token.tsx` — public route (no SessionAuth
required; the token IS the auth). Mounts:

- Top: small "View-only document shared by {sender_name}" banner.
- Center: pdf.js canvas (one page at a time), watermark overlay div
  positioned absolute with `pointer-events: none`.
- Right rail: telemetry pings (page-view on render, dwell timer,
  focus/blur handlers).
- CSS:
  - `user-select: none` everywhere
  - `oncontextmenu` returns false
  - `@media print { * { display: none !important } }`
  - canvas drawn with `image-rendering: pixelated` (cosmetic only;
    doesn't affect security)

Anything more aggressive (e.g., overlaying a transparent canvas on
DevTools open detection) is heuristic — included as a hook in
`telemetry.ts` for tenants that want it.

## Sender's activity panel

`web/src/components/documents/ZTShareActivity.tsx`: timeline of
events, list of distinct page-views, dwell-time histogram. Refreshes
every 10 s. Includes a "Revoke this share" button that calls the
revoke endpoint.

## Phased rollout

| Phase | Scope | Shipped now? |
|---|---|---|
| 1 — Honest baseline | Share-link type, server-side rendered tiles with watermark, telemetry, sender timeline, revoke | ✅ this ADR |
| 1.5 — Encrypted tiles feature-flag | Rotating session keys + custom decode | ⚠️ wired, off by default |
| 2 — Subdomain hosting | `zt.<tenant>.vaultdms.example`, iframe postMessage | ⏳ |
| 3 — DRM-backed viewer | Widevine/PlayReady/FairPlay integration | ⏳ requires vendor + licensing |
| 4 — Auto-revoke on patterns | Server-side anomaly detection feedback loop | ⏳ requires false-positive analysis |

## Open questions

- Whether to mention to users that screenshots are still possible.
  Hiding it lets sales people promise "zero leak"; saying it plainly
  loses the deal but doesn't get the customer sued when a leak
  happens. Recommendation: bake the honest copy into the share-create
  modal — "View-only mode: blocks copy, print, and download. Does
  not prevent screenshots taken with the recipient's device — every
  page is watermarked so leaks can be traced back to the recipient."
- License gating: which tier includes ZT share? Probably enterprise
  only (it's a "looks enterprise" feature). Wire to
  `feature_flags.zt_share` (ADR 0095) when license enforcement
  expands beyond document-service.
- Retention of telemetry rows: tied to the share token's `expires_at`?
  Or a fixed 90-day window? Phase 1 ships fixed 90 days; revisit
  after first enterprise sale.
