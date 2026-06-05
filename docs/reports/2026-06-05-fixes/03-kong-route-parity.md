# 03 — Close Kong route gaps (prod 404s)

**Commit:** `60e9fde` · **Type:** fix (gateway) · **Date:** 2026-06-05

## Summary

A route-connectivity audit found ~21 frontend route prefixes that work in dev
(the Vite per-prefix proxy covers them) but **404 at the Kong gateway in prod** —
Kong's declarative config had drifted from the actual routes and has no
catch-all. It was also missing entire upstreams. Mirrored the known-good Vite
proxy map into `kong.yaml` (+ `routes.yaml`, the archtest source of truth).

## Root cause

Kong (`deploy/gateway/kong.yaml`) enumerates routes explicitly with no fallback.
Over time the frontend gained routes that were never added to Kong, and the Vite
dev proxy (`web/vite.config.ts`) masked the gap. The archtest
(`TestGatewayRoutesMirrorKongConfig`) only checks `routes.yaml ⊆ kong.yaml`
(one-directional), so it never caught handlers absent from **both** files.

## What landed

- **5 new upstreams** Kong lacked entirely: `policy`, `billing`, `intelligence`,
  `graphql-gateway`, `mcp-server`.
- **Fixed 2 mis-attached routes** that 404'd in prod regardless: `/permissions`
  (handlers live in `policy`, not `document`) and `/admin/settings` (`billing`,
  not `document`) — moved off the document upstream.
- **~28 missing prefix routes** added: comments, threads, tags, tasks, clauses,
  contracts, compare, folders, uploads/predict, zt, tenants/metadata-schema,
  compliance, and the `admin/*` intelligence subtrees (compliance, trash, bulk,
  models, training-examples, active-learning, auto-tag-config, tag-suggestions,
  ner-config, ocr-quality, routing-rules, smart-routing-config,
  filing-analytics, anomalies, anomaly-config, platform/db-info,
  tenant/license, tenant/upload-policy) on `document`; ldap, mfa, notifications
  on `auth`; email-configs, event-stream, intake on `connector`; intelligence +
  llm-usage + tenant on `intelligence`; graphql; mcp.
- Mirrored every addition into `deploy/gateway/routes.yaml`.

## Verified

Kong reloads the declarative config cleanly; previously-404 routes now reach
their backends through `:8080` (401/400 backend responses, **not** "no Route
matched"). `TestGatewayRoutesMirrorKongConfig` passes.

## Open follow-ups (noted, not in this commit)

- The archtest is one-directional — a handler→routes.yaml scanner would prevent
  recurrence.
- Session-cookie auth **through Kong** returns 401 even on control routes (the
  app is normally used via the Vite dev proxy, not Kong) — worth verifying
  before relying on Kong in prod.

## Files changed
`deploy/gateway/kong.yaml`, `deploy/gateway/routes.yaml`.
