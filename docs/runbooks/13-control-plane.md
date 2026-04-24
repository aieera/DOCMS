# Runbook 13 — Control plane (provisioning + Stripe + lifecycle)

Operator procedures for tenant provisioning, Stripe webhook handling,
and the 30-day soft-delete → hard-dispose lifecycle.

## Signup → live (DoD: ≤3 min)

1. **Checkout opened** — frontend calls Stripe `/v1/checkout/sessions`
   with `client_reference_id = <future_tenant_id>` (UUIDv7 minted
   client-side) and `mode = subscription`.
2. **Customer pays** — Stripe fires `checkout.session.completed`
   to `POST /internal/v1/stripe/webhook` with signature.
3. **Webhook** ([services/billing/internal/stripe/webhook.go](../../services/billing/internal/stripe/webhook.go)):
   verifies signature, dedupes via `stripe_events`, routes to
   `onCheckoutCompleted`. Marks the subscription `active`, un-suspends
   the tenant route.
4. **Provisioning** — triggered out-of-band by the signup handler
   calling `POST /internal/v1/tenants/provision`. Synchronous path in
   [services/billing/internal/provisioner/provisioner.go](../../services/billing/internal/provisioner/provisioner.go)
   creates: organization row + v1 KEK (`vaultdms/tenant/<uuid>` alias)
   + Redis tenant route + subscription + admin user. OpenSearch index
   + Qdrant collection are created lazily on first document.

Budget: the synchronous provisioner runs ~500 ms against a warm DB.
The long pole is Stripe webhook latency (typically 1-3 s) — the
3-minute SLA is comfortable.

## Stripe webhook idempotency

Every received event is persisted to `stripe_events (stripe_event_id UNIQUE)`
*before* any handler runs. A duplicate `event.ID` → the INSERT's
`ON CONFLICT DO NOTHING` returns `0 rows affected` → we 200 OK and
skip the handler. Stripe retries for 3 days; dedupe must outlive that
(90-day retention via `received_at` GC index).

Verify dedupe after an incident:

```bash
psql "$DATABASE_URL" -c "
  SELECT stripe_event_id, event_type, received_at
    FROM stripe_events
   WHERE received_at >= now() - interval '1 hour'
   ORDER BY received_at DESC LIMIT 20;
"
```

## Payment failure → grace period → suspend

- `invoice.payment_failed` webhook sets subscription status
  `past_due` + `grace_period_ends = now() + 14d`.
- Background sweeper (nightly) moves tenants past their grace window
  to `suspended` and calls `router.Suspend(tenant_id)` — gateway then
  returns 402 on every request.
- Payment recovery: `invoice.paid` webhook clears `past_due` +
  un-suspends via `router.Unsuspend`.

## De-provisioning (30-day soft delete → crypto shred)

### Soft delete

```bash
curl -sk -X POST -H "X-API-Key: $ADMIN_KEY" \
  https://api/internal/v1/tenants/<tenantId>/deprovision
```

Effects:
- `organizations.deleted_at = now()`
- `organizations.dispose_scheduled_at = now() + 30 days`
- `DeprovisionWorkflow` started on Temporal with WorkflowID
  `deprovision-<tenant_id>` (idempotent; re-call is a no-op).

During the window the tenant is read-only-hidden from user-facing
endpoints but still visible on the admin Tenants page with a
"soft-deleted · disposes in X days" badge.

### Undo (before hard dispose)

```bash
curl -sk -X POST -H "X-API-Key: $ADMIN_KEY" \
  https://api/internal/v1/tenants/<tenantId>/undo-deprovision
```

Clears `deleted_at` + `dispose_scheduled_at` and signals the running
Temporal workflow with `undo_deprovision`. Returns 409 if
`disposed_at` is non-NULL (crypto shred already fired — irreversible).

### Hard dispose

Fires automatically when the 30-day timer expires. Activity
`HardDisposeTenant` ([services/workflow/internal/activities/activities.go](../../services/workflow/internal/activities/activities.go)):
1. `UPDATE tenant_keks SET retired_at = now()` for every version of
   the tenant's KEK alias.
2. `UPDATE organizations SET disposed_at = now()`.
3. Cross-service data purge (OpenSearch index, Qdrant collection,
   S3 objects) — best-effort; not blocking because the retired KEK
   already makes all wrapped blobs undecryptable.

Emits `dms.tenant.disposed.v1` with the dispose_id (UUIDv7).

**Not yet automated**: the actual KMS CMK deletion. On AWS KMS, the
shred should call `ScheduleKeyDeletion` with a 7-day minimum window.
Local HKDF manager has no analog; tracked on the ledger.

## Admin Tenants page

Route: `/admin/tenants` ([tenants.tsx](../../web/src/routes/_authenticated/admin/tenants.tsx)).
Displays every org + lifecycle badge, exposes Deprovision / Undo
buttons with a confirm prompt. Calls the same internal endpoints via
the gateway.

## Emergency pause

To freeze the Temporal de-provision timer without code changes:

```bash
temporal workflow terminate --workflow-id deprovision-<tenant_id>
```

Then manually clear `disposed_at = NULL` + restore `dispose_scheduled_at`
in Postgres if the soft-delete should persist, or restore the whole
tenant with the undo endpoint.

## Usage metering

Hourly cron in [services/billing/internal/metering/metering.go](../../services/billing/internal/metering/metering.go):

1. For every tenant in `organizations`: `MeterStorage` / `MeterOCRPages` / `MeterActiveUsers` (+ AI tokens placeholder).
2. UPSERT row into `usage_records (tenant_id, period_start, …)`.
3. If `STRIPE_API_KEY` set AND tenant has `stripe_customer_id`: ship one `billing.MeterEvent` per non-zero metric, stamp `reported_at`. Idempotency key: `<metric>:<tenant>:<period_start_unix>`.

**Previously-silent bug (fixed 2026-04-20)**: `content_blobs`, `ocr_results`, `sessions` all have RLS USING `tenant_id = current_setting('app.current_tenant', true)::uuid`. The metering queries used to run directly on the pool without setting the GUC → every query returned zero rows → the billing dashboard showed `storage_gb = 0` for every tenant. Every metering query now wraps in `pkg/database.WithTenantTx`.

**Stripe Meter config** — each metric needs a Meter configured on the Stripe account with `event_name` matching [metering/stripe_meter.go](../../services/billing/internal/metering/stripe_meter.go) constants: `vaultdms_storage_gb`, `vaultdms_ocr_pages`, `vaultdms_api_calls`, `vaultdms_active_users`, `vaultdms_ai_tokens`.

## Tracked gaps (ledger)

- Temporal wrapper for the **provisioning** side (procedural today;
  DoD-met but not workflow-shaped).
- KMS CMK `ScheduleKeyDeletion` on AWS; equivalent on Vault Transit.
- Cross-service data purge activities wired into `HardDisposeTenant`
  (docs / search / qdrant / s3).
