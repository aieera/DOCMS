# ADR 0117 — Mobile App (Expo): auth bridge, viewer, approvals, capture, push

- **Status**: Accepted
- **Date**: 2026-07-02
- **Relates to**: ADR 0068 (tasks), ADR 0086 (notification matrix), ADR 0112/0113/0116 (add-in session-bearer precedent), §8.1 (no token in insecure storage / archtest C2)

## Context

`mobile/` is an Expo SDK 51 / expo-router app that already shipped capture
(multi-page scan → PDF → the standard initiate/PUT/complete/CreateVersion
chain, with a durable SQLite upload outbox), search, and flat browse. This
ADR covers the decisions that completed it: how a cookie-less native
client authenticates to every backend service, what the viewer renders,
what "approve" targets, how push reaches a phone, and where secrets live.

Two premises from planning did not survive contact with the code: "E2.5"
appears nowhere in the repo (external label; capture nonetheless exists),
and "notification service ✅" is true only for in-app + email — no
FCM/APNs/web-push transport existed anywhere.

## Decisions

### 1. Auth: password login → session bearer, honored by shared middleware

The app POSTs `/api/v1/auth/login` and holds the returned session token,
sending `Authorization: Bearer <session>` on every call — the same shape
the Office/Google add-ins use. Previously only the auth service accepted
that; `pkg/middleware.SessionAuth`/`SessionAuthOptional` read **only the
`dms_session` cookie**, so document/search/storage/notification 401'd
every mobile request.

The shared middleware now falls back to a **non-`vdms_` Bearer** when no
session cookie is present (`sessionTokenFromRequest`). Cookie precedence
is unchanged (zero web-path change); `Bearer vdms_…` remains an API key
and is never treated as a session. `CSRFDoubleSubmit` exempts a Bearer
caller **only when the request carries no `dms_session` cookie**: with no
ambient credential there is nothing for a cross-site request to ride
(forms can't set Authorization; cross-origin fetch needs a CORS
preflight), while a browser holding the cookie stays fully CSRF-checked.

Session policy the app is built around: 24 h TTL with sliding renewal,
7-day hard max (weekly re-login even with biometrics), 5-concurrent-
session cap, server-side revocation → the client treats any 401 as
"sign in again" (interceptor clears the session).

### 2. Secrets: SecureStore only; biometric unlock; C2 extended to mobile

The session token, user, and tenant persist in `expo-secure-store`
(Keychain / EncryptedSharedPreferences) — the §8.1 analogue of the web's
httpOnly-cookie rule. AsyncStorage holds only the react-query display
cache. A new archtest (`TestPhaseC2_NoAuthTokenInMobileInsecureStorage`)
fails the build if an auth-shaped key ever appears in AsyncStorage.
An optional biometric gate (`expo-local-authentication`) guards cold
starts; it degrades open (not locked-out) when no biometrics are
enrolled, because the session itself is the credential, not the biometric.

### 3. Viewer: raw blob over `/documents/{id}/content`; no preview service

The preview service's `/previews/*` endpoints are not gateway-routed
(STATE: "orphaned"), so the viewer renders the raw current version:
`react-native-pdf` for PDFs and `<Image>` for images, both streaming
`GET /api/v1/documents/{id}/content` with the bearer header — that route
decrypts envelope-encrypted blobs server-side, avoiding the presigned-URL
dance (and its relative decrypt-stream redirect). Other MIME types fall
back to download-and-share (`expo-file-system` + `expo-sharing`).
Routing `/previews/*` and a thumbnail grid remain follow-ups.

### 4. Approve = tasks inbox + document lifecycle

(The planning premise "workflow service has zero definitions" turned out
stale — it has approval/DSR/retention/review/saved-search workflows —
but the right mobile approve surfaces are still the two below: the
tasks API and the lifecycle action, both document-service-owned.)

"Approve from mobile" targets: `GET /api/v1/tasks/mine` with
`POST /tasks/{id}/complete|reopen` (ADR 0068) as the inbox, and
`POST /api/v1/documents/{id}/lifecycle {action: approve|reject}` on
documents sitting `in_review` (edit-permission gated server-side).
There is no pending-signatures-for-me endpoint; signature approvals stay
notification-link driven.

Wire-format note (grpc-gateway/protojson): the lifecycle route takes the
proto ENUM NAMES (`LIFECYCLE_ACTION_APPROVE`, not `approve` — lowercase
is silently discarded), and `lifecycle_state` in document reads
serializes as `LIFECYCLE_STATE_*`; the app maps both at the API-client
boundary.

### 5. Comments + annotations over REST; notes are the authorable subset

The document screen's Comments panel uses the threaded comment REST
(create/reply/resolve); Annotations lists the current version's
annotations and can author a **page-pinned note** (`pdf_markup` /
`kind:"note"`, empty rects) — the same vocabulary the web viewer renders.
Rect/draw markup needs canvas tooling and stays web-only. The
collaboration WebSocket is untouched: REST is complete; the WS is
live-update sugar.

### 6. Push: Expo push service; device registry on the notification service

The deferred "adapters land with the mobile work" landed as
`pkg/notifications.ExpoClient` — batched POSTs to Expo's push API, which
fronts FCM **and** APNs (no Google/Apple credentials to manage;
`SEDOC_EXPO_PUSH_URL` overrides for tests/proxies). The notification
service gained a `push_devices` table (migration 000002) +
`POST/GET/DELETE /api/v1/notifications/devices`, and its Deliver loop
fans out to the recipient's active devices after the in-app insert,
gated by the ADR 0086 decide-matrix (`push` channel) with the flat
`push_enabled` preference as the fallback — exactly the email pattern.
Tokens Expo reports `DeviceNotRegistered` are revoked so the fleet
self-heals. Native `push_fcm.go`/`push_apns.go` remain future options
behind the same transport interface. Delivery is opportunistic: the
in-app row is the source of truth; push failures only log.

The app registers on login and on every unlocked start (idempotent
upsert), and notification taps deep-link to the referenced document.
Expo Go cannot receive remote pushes (SDK 51+) — a dev/standalone build
is required, same as the native scanner.

### 7. Offline: read cache + the existing upload outbox

Query results persist to AsyncStorage via the react-query persister
(24 h), so browse/search/document/task/notification screens render
last-known state offline. Writes stay online-only except capture, whose
SQLite outbox with NetInfo auto-drain already existed.

## Consequences

- The Bearer-session bridge also un-breaks the Office/Google add-ins
  beyond the auth service — they were relying on the same gap.
- Anyone with a SeDoc session can register a push device; there is no
  per-tenant device quota yet (follow-up if abused).
- The mobile UI is review-verified only: no Expo host, device, or
  `node_modules` exist in this environment (CI runs `npm audit` only —
  adding `npm ci && npm run typecheck` to CI is a follow-up). The Go
  surfaces are unit-tested to different depths: middleware bridge and
  Expo client fully; the device REST handlers at the validation/auth
  layer only (repository-backed paths need the integration suite).
- Weekly forced re-login is inherited session policy, not a mobile
  choice; a refresh-token mechanism would be its own ADR.

## Amendment (2026-07-03) — review hardening

An adversarial review of this task confirmed 18 findings, all fixed:
the login response contract (session_token / user.tenant_id, plus an
MFA TOTP step), the grpc-gateway enum mapping above, session hydration
moved to the root layout with the 401 interceptor scoped to
token-carrying requests (deep links no longer wipe the keychain),
react-native-blob-util added for react-native-pdf, the persisted query
cache cleared on logout (cross-account leak on shared devices), comment
threading aligned to the flat wire shape, task status `done`, and on
the backend: the flat per-user prefs finally got a real table
(notification migration 000003 — the old query hit matrix-shaped
columns, always errored, and the swallowed error silently disabled
every email/push delivery), push joined the no-rows default matrix
policy (a registered device is the opt-in), push now fails CLOSED on a
preferences error, push_devices gained the users FK, and DSR
email-recipient payloads branch to a direct SMTP send instead of dying
on the UUID insert. Follow-ups: revoke push devices on session
revocation; per-tenant device quotas.
