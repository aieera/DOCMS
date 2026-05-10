# ADR 0073 — Mobile + In-Person Signing Modes

Date: 2026-05-10
Status: Accepted
Closes: blueprint §11.4 — "Mobile signing" + "In-person signing" + "Tamper detection"

## Context

The signature surface so far covers three remote ceremonies:

- **Internal AdES** ([ADR 0070](0070-qes-tsp-integration.md)) — emailed magic-link
  per signer, click-to-sign / drawn / typed.
- **eIDAS QES** (also ADR 0070) — redirect to Swisscom / Intesi /
  InfoCert, return after the QTSP signs.
- **Third-party** ([ADR 0071](0071-third-party-esign-connectors.md)) —
  hand off to DocuSign or Adobe Sign.

All three assume a separate device per signer and a network round
trip between signers. Two scenarios from blueprint §11.4 are still
unsupported:

1. **Mobile signing** — a signer opens the magic-link on a phone and
   signs by drawing with their finger. The current flow renders a
   desktop-first page (max-w-3xl, no touch-canvas, no biometric
   capture); on a phone it works visually but the only available
   "draw" option is the typed/click fallback.
2. **In-person signing** — both signer and witness are physically
   together with a single device (typically a tablet at a counter,
   notary office, or hospital admissions). Today this would require
   each to open a separate magic-link in a separate browser, which
   defeats the workflow. The schema already supports a `witness`
   role on `signature_signers` (000001 initial schema) but no
   ceremony surfaces it.

§11.4 also asks for **tamper detection**: a SHA-256 of the document
bytes at the moment of signing, stored on the signature record so
any post-sign modification of the underlying blob is detectable
even before invoking the full PAdES validator.

## Decision

### Three signing modes, one signature_request

We extend `signature_requests` with a `signing_mode` discriminator
rather than minting a new request type:

| `signing_mode` | Ceremony |
|---|---|
| `remote` (default) | Per-signer magic-link, separate devices. Existing flow. |
| `mobile` | Per-signer magic-link, mobile-optimized UI, finger-drawn SVG capture. Same backend; client picks the layout from `navigator.maxTouchPoints` and the route. |
| `in_person` | Single device, sequential signer→witness on the same session. New `/sign/in-person/$requestId` route + `POST /signatures/requests/{id}/in-person/sign` endpoint. |

The mode is set at request-creation time; the existing
`sequential` flag still controls multi-signer order independently.
For `in_person` we **require** `sequential = true` because the
ceremony itself is sequential — the witness countersigns *after*
the signer in the same session.

### Per-signer biometric capture

`signature_signers` gains:

- `signature_svg_path TEXT NULL` — the path data captured by the
  client's `<canvas>` pointer-event recorder, serialized as an
  SVG `<path d="...">` string. Stored as text rather than rasterized
  bytes so it stays small (~2-5 KB typical), scales without quality
  loss when embedded in the PDF, and can be re-rendered at print
  time for the certificate of completion.
- `device_kind TEXT NULL` — `phone` / `tablet` / `desktop`, derived
  client-side from `matchMedia('(pointer:coarse)')` + viewport. Used
  for evidence on the certificate, not for any auth decision.
- `signed_doc_hash_sha256 TEXT NULL` — the SHA-256 of the document
  bytes the signer saw when they signed. Persisted alongside the
  signature so we can detect a content swap between signer and
  witness in the `in_person` ceremony.

### Tamper-detection on the request

`signature_requests` gains `final_hash_sha256 TEXT NULL` — the hash
of the document bytes at the moment the request transitions to
`completed`. The `Verify` flow compares this against a current
re-hash of the document; any mismatch is reported as
`tamper_evident = false`. The PAdES-LTV validator (ADR 0072)
remains the authoritative answer for signed PDFs; this hash is the
defense-in-depth answer for the simple/drawn AdES case where the
PDF itself isn't carrying a CMS signature.

### In-person ceremony

The `/sign/in-person/$requestId` route:

1. Loads the request (no per-signer token — the URL is the
   ceremony entry, not a per-signer link). The route is auth-gated
   like the rest of `/_authenticated/*`; only a tenant member with
   permission on the document can open it.
2. Walks the signer list in `order_index` order. For each signer:
   1. Show the signer's name + a reminder of who's signing.
   2. Render the signature pad. Capture SVG + device kind.
   3. POST `/signatures/requests/{id}/in-person/sign` with
      `{ signer_id, svg_path, device_kind }`.
   4. The backend verifies that the previous signer in order is
      already signed (or this is signer 1), records the signature,
      and re-hashes the document bytes for both `signed_doc_hash_sha256`
      (per signer) and `final_hash_sha256` (set on the last
      signer's commit).
3. After the last signer, flips the request to `completed` and
   navigates to `/sign/done`.

The endpoint refuses out-of-order signing (HTTP 409 with the
expected next signer's id in the body) so the UI can recover by
fast-forwarding to the right step.

### Backend layout

```
services/signature/internal/
  handler/inperson.go      — POST /signatures/requests/{id}/in-person/sign
  service/inperson.go      — orchestration (order check, hash compute,
                             outbox event for last-signer completion)
  service/hash.go          — SHA-256 of document bytes via the
                             storage gRPC client (same path the QES
                             flow uses for the to-be-signed bytes).
```

`handler/handler.go`'s existing `recordSignature` accepts the new
`svg_path` and `device_kind` fields too, so the remote/mobile
ceremony writes the same biometric record without needing the
in-person endpoint.

### Frontend layout

```
web/src/components/signatures/SignaturePad.tsx
  Canvas + pointer-events recorder. Emits SVG `path d` on submit.
  Touch-first sizing (h-[40vh] on mobile, h-64 desktop), with
  Clear / Done controls. No external lib — the recorder is ~80 LOC
  and avoids the perfect-freehand bundle weight.

web/src/routes/_authenticated/sign.$requestId.$signerId.tsx
  Existing route, made responsive (mx-auto px-4 instead of p-6,
  type-selector grid collapses to a single column < sm).
  Wire the SignaturePad in for type='simple' and type='advanced'.

web/src/routes/_authenticated/sign.in-person.$requestId.tsx
  New route. Sequential walk through the signer list with the
  SignaturePad re-mounted per step. The "next signer please hand
  the device over" affordance is a full-bleed prompt between
  signers so the device hand-off is unambiguous.
```

Mode is derived from the URL — `/sign/.../...` is the
remote/mobile route, `/sign/in-person/...` is the tablet route.
We do not add a runtime mode switcher to either; the sender chose
the mode when the request was created.

### Tamper-detection wiring

- On the `in_person` last-signer commit, compute the hash and
  write `final_hash_sha256`.
- On the existing `RecordSignature` (remote/mobile) last-signer
  commit, do the same.
- `Verify` (the existing tenant-scoped lookup, not the PAdES one)
  re-fetches the document via the storage proxy, hashes it, and
  reports `tamper_evident = (final_hash_sha256 == current_hash)`.
  If `final_hash_sha256` is null (request still in flight) the
  check returns `tamper_evident = true` since there's nothing to
  diverge from yet.

## Consequences

- Schema gains 4 columns; no breaking change to existing AdES /
  QES / esign flows. Old rows back-fill `signing_mode='remote'`
  and leave the new fields NULL.
- The SVG path is captured client-side and trusted at face value
  — there's no server-side biometric verification (no claim that
  this is the same finger / pressure pattern as last time). The
  legal weight of a drawn signature is identical to the existing
  drawn-image path in §11.2; this just makes the capture
  comfortable on a phone and the storage smaller.
- The in-person endpoint relies on session auth, not magic-link
  tokens. That means a tenant member with read access to the
  document who isn't the named signer could initiate the ceremony.
  This matches the workflow (a counter clerk opens the request on
  the tablet, then hands it to the signer); it does mean the
  audit trail records the *device* user as the session principal
  while the *signer* identity comes from the named signer row. The
  certificate of completion makes this distinction explicit.
- Tamper-detection via SHA-256 hash gives us a fast positive on
  modification, but it's defense-in-depth — not a replacement for
  PAdES-LTV (ADR 0072). The hash check catches blob swaps; PAdES
  catches PDF-internal tampering with cryptographic precision.
