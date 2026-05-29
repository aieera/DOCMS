# Accepted Risk — PT-YYYY-NNN

**Finding ID:** PT-YYYY-NNN
**Severity (as reported):** Critical / High / Medium / Low
**Source engagement:** YYYY-MM &lt;vendor&gt;
**Accepted on:** YYYY-MM-DD
**Next review:** YYYY-MM-DD (must be within 12 months)

## Finding (redacted)

One-paragraph description. Strip exact reproduction steps and any
detail that would help an attacker outside our own engineering
context. The full finding stays in `docs/security/pentests/findings/`.

## Why we are not fixing

The argument for non-remediation. Examples of what belongs here:

- "Fix requires a protocol change we don't control" — name the
  protocol, who controls it, what we've requested.
- "Mitigation cost outweighs residual risk under current threat
  model" — quantify both sides honestly.
- "Already addressed by upstream control" — name the control.

A reluctance to do the work is **not** a valid reason.

## Compensating controls

What we have in place that reduces the residual risk. Tie each
control to a concrete artifact (config file, monitoring rule, code
path) so someone reviewing later can verify it still exists.

- Control 1 — &lt;description, link to code/config&gt;
- Control 2 — &lt;description, link to code/config&gt;

## Detection

How we'd notice if this risk is being exploited.

- Log / metric / alert &lt;name&gt; — &lt;where it fires&gt;

## Sign-offs

- **CTO:** &lt;name&gt; — YYYY-MM-DD
- **Security lead:** &lt;name&gt; — YYYY-MM-DD
- **Service tech lead:** &lt;name&gt; — YYYY-MM-DD

## Review log

Updated at each review:

| Date | Reviewer | Outcome | Notes |
|---|---|---|---|
| YYYY-MM-DD | &lt;name&gt; | Still accepted / Re-opened for fix / Conditions changed | &lt;notes&gt; |
