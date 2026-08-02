# Intelligence admin surface — optimization design

Date: 2026-08-02
Status: Approved by user (brainstorm session)
Scope: web admin Intelligence section (8 hub cards, ~14 route files) +
two small Go-backed config UIs. NO gateway/proxy changes — the audit
verified all 16 API prefixes route correctly end-to-end.

## Decisions (user-confirmed)

| Question | Decision |
|---|---|
| Role gate contradiction | **Let compliance officers in** — frontend allowlist mirrors backend authorization |
| Missing config UIs (smart-routing-config, anomaly-config) | **Wire both** using the existing unused endpoints/client fns |
| Orphan /admin/intelligence hub | **Redirect to /admin** |

## Workstreams

1. **Role access.** `web/src/routes/_authenticated.tsx` guard gains a
   per-path allowlist: `compliance_officer` may open `/admin` (hub),
   `/admin/ocr`, `/admin/pii-scanning`, `/admin/tagging`,
   `/admin/intelligence/anomalies` — the pages whose Go handlers already
   grant that role read access. Admin hub filters cards by role;
   sidebar `/admin` entry adds `compliance_officer`. Everything else
   under `/admin/*` stays admin/owner. The OCR read-only banner
   (ocr-config.tsx:177) becomes reachable.
2. **Error states.** compliance.tsx, compliance-config.tsx,
   auto-tag.tsx, ner-config.tsx, tenant/ai.tsx: replace
   `isLoading || !data → "Loading…"` with distinct loading / error
   (ErrorState + retry) / content branches. filing-analytics.tsx:
   `if (!data) return null` → error branch.
3. **Broken deep link.** tag-review.tsx: replace
   `<a href="/workspaces/_/documents/{id}">` with a click handler that
   resolves the document's workspace (getDocument) and navigates via
   the router.
4. **Small fixes.** usage.tsx dev-TODO footer removed; anomalies.tsx
   "Open findings" tile relabeled "Findings reported" (it sums
   anomalies_found across completed reports, resolved included);
   models.tsx dead hover class; auto-tag.tsx Save dirty-guard;
   routing-rules.tsx toggle onError.
5. **Config UIs.** Routing rules page gains a Configuration card bound
   to GET/PUT /admin/smart-routing-config (existing
   getSmartRoutingConfig/updateSmartRoutingConfig); Anomalies page
   gains Scan settings bound to GET/PUT /admin/anomaly-config. Field
   set = whatever the backend DTOs expose; dirty-guarded Save + revert;
   admin/owner-only render (write endpoints are admin-gated).
6. **Layout normalization.** ai.tsx, ocr.tsx, tagging.tsx,
   pii-scanning.tsx get PageHeader above the tab strip (ingestion.tsx
   pattern); child tab components drop their own `p-6`/`mx-auto`
   wrappers so each page presents one width; filing-analytics gets the
   same header treatment.
7. **Anomaly detail modal → Sheet** (role=dialog, focus trap, Esc);
   the two raw `<pre>` JSON dumps become formatted key/value lists
   (fall back to <pre> only for unknown nesting).
8. **Names + N+1.** filing-analytics resolves top_folder_by_category
   folder ids to names; routing-rules fetches folders only for
   workspaces actually referenced by rules.
9. **Pagination.** compliance findings list gets limit/offset paging
   (server already supports); anomalies/models/staged-items keep their
   caps but state "showing first N".
10. **Cleanup.** intelligence/index.tsx → redirect to /admin;
    hardcoded violet-* classes in compliance.tsx / ocr-review.tsx moved
    to theme tokens (primary).

## Out of scope

Backend behavior changes (except none needed — config endpoints exist);
gateway/kong/vite files; the /admin/tags standalone URL; the
admin/compliance naming collision (different feature, already
redirects); mobile.

## Verification

tsc, npm run build, vitest (existing + new: guard allowlist, error
branches, config sections), lint; live dev-stack spot-check of the two
new config UIs and the compliance-officer path.
