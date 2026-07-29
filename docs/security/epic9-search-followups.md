# Epic 9 — search service: fixes + residual note

The Epic 9 adversarial review of the search service (the prime cross-tenant
leakage surface — it aggregates content across documents) confirmed 8 distinct
defects (7 high). **All 8 are fixed on this branch.**

## Fixed
| # | Sev | What |
|---|-----|------|
| 6 | HIGH | Search/suggest/autocomplete derived the caller's ACL **groups from the client-controllable `X-Group-IDs` header** — a user could add groups they're not in and read those groups' documents. Now resolved authoritatively (`callerGroups`): the session groups SessionAuth loaded from `group_members`, or a DB lookup for internal-service callers (saved-search alert). Header never trusted. |
| 7 | HIGH | Smart-folder list + promote gate used the client-controllable **`X-Workspace-IDs` header** — a user could list/pin folders into workspaces they're not in. Now resolved authoritatively from `workspace_members` (`callerWorkspaces`). |
| 1 | HIGH | Unknown facet names were used **raw as the terms-aggregation field**, so `facets:["share_tokens"]` / `["readable_by_users"]` enumerated a document's share tokens / ACL principals. Now dropped (the `FacetSizes` + `ResolveFacet` registry is the allowlist), as the doc comment always claimed. |
| 2 | HIGH | Folder/workspace permission **revoke updated only the mixed `readable_by`**, leaving the split `readable_by_groups`/`readable_by_users` (which the query actually matches on) stale → a revoked group still matched. `UpdateReadableBy*` now update all three fields from the event's split data (threaded through the debouncer). |
| 3 | HIGH | Semantic/hybrid (Qdrant) hits were returned **without re-checking the OpenSearch ACL**; the Qdrant payload's `readable_by` isn't updated on revoke, so a revoked user kept getting the doc on the vector path. Semantic hits are now re-verified against the authoritative OpenSearch ACL (`filterSemanticByACL` + `BuildVisibilityByIDsQuery`), failing closed on error. |
| 4 | HIGH | The semantic/hybrid path omitted the **share-token isolation guard** — `readablePrincipals` injected `"everyone"` for anonymous share-link followers, leaking every tenant-wide-visible document instead of the one shared doc. The vector path now skips anonymous share-followers (lexical is already token-scoped). |
| 5 / 8 | HIGH/MED | The **facet cache key omitted `ShareToken`** while facets are share-token-scoped, so anonymous followers of different tokens cross-served each other's buckets. Token added to the key. |

## Residual note (not a security gap)
- #3 root cause: the Qdrant point **payload** `readable_by` is only rewritten by
  the intelligence service on re-embed (content change), so it stays stale after
  a permission revoke. The security leak is fully closed by the authoritative
  OpenSearch ACL re-check on every semantic/hybrid response. Updating the Qdrant
  payload on permission events (so the vector query itself is precise) would be a
  future efficiency improvement — it requires the intelligence service (which
  owns Qdrant writes) to consume `dms.permission.changed.v1`.
