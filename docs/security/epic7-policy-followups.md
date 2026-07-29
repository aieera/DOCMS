# Epic 7 — policy/authorization service: fixes + deferred follow-ups

The Epic 7 adversarial review of the policy service (the OPA decision point every
other service calls) confirmed 3 distinct defects (all high-impact — an
authorization oracle bug affects every consumer). This branch fully fixed the
role-spoof bypass and bounded the two cache-staleness bugs; the complete fix for
the caches needs membership-change events that no service currently emits.

## Fixed on this branch
| # | Sev | What |
|---|-----|------|
| 1 / 4 | HIGH | `POST /permissions/check` forwarded the client request-body `context` map straight into the OPA input. Because the endpoint is session-authenticated and the caller controls the body, any user could set `context.user_role=owner/admin` — rego **Rule 6 then grants every capability on every resource** and exempts them from the disposed/deactivated/clearance **deny** gates — or fabricate `folder_id`/`workspace_id` to force the Rule 3/4 cascade, or spoof `user_clearance`/`user_status` to defeat the classification and deactivation gates. **Fix:** the handler now builds the decision context server-side as `{user_role: u.Role}` (the authenticated role) and ignores client-supplied context entirely. The real frontend `checkPermission` sends no context, so this is zero-impact; the trusted gRPC path (document service, context from the JWT + resolved resource) is unchanged. |
| 2 | HIGH (bounded) | `user->groups` cache had no membership-change invalidation (`InvalidateUserGroups` was dead code — zero callers), so a user removed from a group kept the group's grants for up to the 60s TTL. **Mitigation:** these membership caches now use a short `MembershipTTL` (5s) to bound the stale-access window. |
| 3 | HIGH (bounded) | `user->workspaces` cache (incl. role) had **no** invalidation function at all, so an admin demoted to member/removed kept admin over the whole workspace for up to 60s. **Mitigation:** short `MembershipTTL` (5s); added the missing `InvalidateUserWorkspaces` for when events exist. |

## Deferred (tracked) — complete fix for #2 / #3
Immediate (event-driven) invalidation of the `user->groups` and `user->workspaces`
caches requires membership-change events that **do not exist today**:

- `group_members` and `workspace_members` are mutated in another service; a
  repo-wide search finds **no** `dms.group.member.*` / `dms.workspace.member.*`
  (add/remove/role-change) events. The policy `PermissionCacheInvalidator`
  subscribes only to `dms.permission.>`.
- Complete fix: emit membership-change events from the service that owns those
  tables, then have `PermissionCacheInvalidator` call the now-wired
  `InvalidateUserGroups` / `InvalidateUserWorkspaces`. Until then, `MembershipTTL`
  (5s) is the only invalidation and bounds the exposure.
