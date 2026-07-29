# Epic 10 — workflow service: fixes + deferred note

The Epic 10 adversarial review of the workflow service (Temporal orchestration:
approvals, reviews, DSR/retention, routing) confirmed 6 findings that reduce to
3 distinct defects, one CRITICAL. All 3 are fixed on this branch.

## Fixed
| # | Sev | What |
|---|-----|------|
| 1 / 2 / 6 | CRITICAL | **Approval-authz bypass.** `CompleteStep`/`signalStep` forwarded an approve/reject/delegate signal to the workflow with NO entitlement check, and `ApprovalWorkflow.executeStep` acted on `signal.Outcome` without comparing `signal.ActorID` to the assigned approver — so any authenticated tenant member could settle (or self-approve) any pending approval by posting to `/instances/{id}/signal`. The workflow now honors a human signal ONLY when it comes from the entitled actor: approve/reject/delegate must come from the resolved assignee/delegate (`effective`), recall from the initiator; anything else is ignored and the step keeps waiting. `ParallelApprovalWorkflow` now counts a vote only from an assigned approver, once each (no quorum-stuffing / repeat-voting). |
| 3 / 5 | HIGH | **Cancel-authz bypass.** `cancelInstance`/`CancelInstance` had no actor check, so any tenant member could cancel any in-flight workflow by id (cancel is the documented initiator/admin override). Now gated on `inst.InitiatedBy == actor || isAdmin(role)`, mirroring `RecallInstance`. |
| 4 | HIGH | **Rego injection / SSRF.** `EvaluateConditionRego` compiled a definition-authored `condition_rego` with OPA's DEFAULT capabilities (`http.send`, `net.*`), and the definition is authorable without a role check — so a condition could `http.send` to internal / cloud-metadata hosts from the trusted Temporal worker. The evaluation now runs under a restricted capability set that removes `http.send`, `net.*`, and `opa.runtime`, bounding the expression to pure computation over the document-metadata input. |

## Deferred (tracked, NOT currently reachable)
- **`ReviewWorkflow` reviewer-decision binding** (`workflows/review.go`): the
  `ReviewerDecided` handler reads `d.ReviewerID` from the signal payload and gates
  only against the pending-reviewer set — it is not bound to an authenticated
  caller, so whoever delivers the signal could decide on behalf of any assigned
  reviewer. **There is no HTTP endpoint that sends `ReviewerDecidedSignal` today**
  (unlike `signalStep`, which stamps `ActorID` from the session), so it is not
  exploitable. When such an endpoint is added it MUST stamp the authenticated user
  and the handler MUST verify `d.ReviewerID == that user` — exactly as
  `ApprovalWorkflow` now does for `StepSignal`. In-code `SECURITY NOTE` marks the
  spot.
