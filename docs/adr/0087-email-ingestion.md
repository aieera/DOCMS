# ADR 0087 — Email ingestion

Date: 2026-05-13
Status: Accepted (the blueprint reserved ADR 0078 for this prompt
but 0078 was already taken by NER-pipeline; reuse 0087 — same
shift the saved-search-alerts ADR did with 0085. Document number
mismatch with the prompt is intentional.)

## Context

§12.5 of the product blueprint requires customers to be able to pull
their email into VaultDMS so messages + attachments become first-
class documents alongside everything else they file by hand. Three
source modalities must be supported:

1. **Microsoft 365 (Graph API)** — for tenants on Exchange Online.
2. **Google Workspace (Gmail API)** — for tenants on Gmail.
3. **IMAP** — for everyone else (legacy on-prem Exchange, Fastmail,
   ProtonMail bridge, anything that exposes IMAP IDLE / polling).

The OAuth halves for #1 and #2 already exist in
`services/connector/internal/providers/{microsoft,google}` —
`PollInbox` and `PollGmail` are stub methods that fetch metadata
from the API but don't (yet) hand the result to anything.

## Decision

### Data model

Two new tables, both per-tenant + RLS-isolated:

`email_ingestion_configs`
```
tenant_id, id, source (microsoft|gmail|imap), label, active,
oauth_token_ref, imap_host, imap_user, imap_password_encrypted,
target_workspace_id, target_folder_id,
poll_interval_seconds (default 300),
last_run_at, last_error,
created_by, created_at
```

`email_messages`
```
tenant_id, id, config_id, source_message_id (unique-per-config),
thread_id, subject, sender, recipients (jsonb), received_at,
document_id (FK into documents.id; NULL if not yet materialised),
attachment_document_ids UUID[],
ingested_at
```

The `source_message_id` UNIQUE constraint per `(tenant_id,
config_id)` is what makes ingestion idempotent — re-polling the
same Gmail thread doesn't re-create the document.

### Ingestion worker

A goroutine inside the connector service ticks every 30s, loads all
active configs whose `last_run_at + poll_interval_seconds <= now()`,
and dispatches each config to its provider-specific handler:

- `microsoft` → `Connector.PollInbox`
- `gmail` → `Connector.PollGmail`
- `imap` → a new `imap.Poller` built on `github.com/emersion/go-imap`

The handler returns a slice of envelope structs:

```go
type EmailEnvelope struct {
    SourceMessageID string
    ThreadID        string
    Subject         string
    From            string
    To              []string
    Date            time.Time
    BodyText        string
    BodyHTML        string
    Attachments     []EmailAttachment
}
```

For each envelope:

1. Insert into `email_messages` (`ON CONFLICT DO NOTHING` keyed on
   the unique constraint — duplicates from re-poll just no-op).
2. If the insert created a row, call out to the document service
   to materialise:
   - The body becomes a new document. Subject is the title; body
     as `text/markdown`; sender, recipients, date land in the
     document's custom-metadata fields (`email.from`,
     `email.to`, `email.received_at`, `email.thread_id`).
   - Each attachment becomes its own document under the same
     folder, with `email.parent_message_id` pointing back to the
     body document so the document UI can render them as a group
     in a later wave.
3. Update `email_messages.document_id` + `attachment_document_ids`
   with the resulting UUIDs.

If document creation fails, the email_messages row stays with
`document_id = NULL`. The worker re-tries on the next tick because
the `INSERT ON CONFLICT` guard keys on `source_message_id` not
`document_id` — same envelope, retry materialisation. Bounded by an
exponential backoff held in the config row (future wave).

### Thread → folder mapping

v1 is one-config-one-folder: every email ingested through a given
config lands in that config's `target_workspace_id /
target_folder_id`.

Rule-based mapping (regex on subject / sender / header) is queued
for §12.5b — the table `email_routing_rules` will be added then,
with a per-config evaluation order. The current data model has the
`thread_id` column so a future "all replies in a thread land where
the parent landed" rule can compute against it without a migration.

### IMAP fallback

`emersion/go-imap` is the de-facto Go IMAP library. The poller
opens a single connection per config, runs `SELECT INBOX`,
fetches `UID FETCH <last_seen>:* (UID ENVELOPE BODY[]
INTERNALDATE)`, and turns each result into the same EmailEnvelope
shape. The IMAP password is stored at rest encrypted under the
tenant KEK — same convention as the LLM API key path in
`services/intelligence`. Plaintext is never returned in any
response.

IMAP credentials are validated on save with a probe `LOGIN` →
`LOGOUT`. Failure returns 400 with the IMAP server's error so the
admin can fix it before the worker tries.

### OAuth provisioning

The Microsoft + Google admin tiles already exist at
`/admin/connectors`. The new `/admin/integrations/email` page reuses
that OAuth flow — `Authorize…` opens the connector's OAuth URL in a
popup, and on callback the connector callback writes
`connector_configs(tenant_id, provider)` and we surface it as
selectable in the email-config form.

### Admin API

```
GET    /api/v1/admin/email-configs           — list configs (no secrets)
POST   /api/v1/admin/email-configs           — create
PATCH  /api/v1/admin/email-configs/{id}      — edit (active flag, mapping, interval)
DELETE /api/v1/admin/email-configs/{id}      — disable + tombstone
POST   /api/v1/admin/email-configs/{id}/run  — kick a one-off poll
GET    /api/v1/admin/email-configs/{id}/stats — counts + last_run_at + last_error
```

## Consequences

- We rely on `services/document` having a "create document from
  byte stream + custom metadata" gRPC. Today it does; the email
  worker will use the same path the bulk-import (§ADR 0075) tool
  uses.
- Per-config polling is independent — a misconfigured IMAP host
  won't block Gmail polling for the same tenant.
- Attachments inherit the config's folder; nested attachment
  trees (mail inside a `.eml` attachment) are flattened to one
  level for v1. The ADR explicitly accepts this — recursive
  unfurling is a customer ask we'll honor when one shows up.
- 7-day mailbox retention is not enforced at the worker layer —
  customers configure that on their own mail server. We pull
  whatever shows up. If a customer wants only the last 30 days,
  they restrict the OAuth scope / IMAP filter on their side.

## Not chosen

- **MIME-direct ingestion (.eml as the document)** — we picked a
  markdown body + attachments-as-documents because that's what
  surfaces best in the existing document list. A separate
  `email.raw_eml` blob can be added in the document's storage
  envelope when a customer needs the original.
- **Push (Microsoft Graph subscriptions / Gmail watch)** — both
  providers support push notifications instead of polling. Out
  of scope for v1 because the webhook receiver requires
  publicly-reachable HTTPS with cert validation that doesn't
  exist in self-hosted deploys. The polling design works
  everywhere; push is a §12.5c performance optimisation.
- **Bidirectional sync** (delete a doc → delete the email) —
  explicitly not in scope. Mail is the source of truth; VaultDMS
  is the archive.
