# VaultDMS Security Posture — YYYY

This document summarizes VaultDMS's most recent independent security
assessment. It is intended for prospects, customers, and procurement
teams evaluating VaultDMS.

For deeper diligence (SOC 2 report, raw pentest report, DPA), contact
security@&lt;company&gt;.

## Most recent penetration test

| | |
|---|---|
| **Vendor** | &lt;vendor name&gt; |
| **Engagement window** | YYYY-MM-DD to YYYY-MM-DD |
| **Scope** | &lt;e.g. web application, public API, admin surface, tenant isolation, key-management boundary&gt; |
| **Methodology** | Black-box + authenticated grey-box; OWASP ASVS L2 baseline |
| **Outcome** | &lt;e.g. "N findings identified; all remediated; retest verdict: Clean."&gt; |
| **Retest completed** | YYYY-MM-DD |

## What was tested

High-level only. Do not enumerate component versions or surface
specifics that aid an attacker.

- Multi-tenant isolation between customer workspaces
- Authentication and session management (SSO, MFA, service accounts)
- Authorization (role-based access, document-level permissions, legal
  hold enforcement)
- API surface (REST and GraphQL gateway)
- Document handling pipeline (upload, virus scan, preview, OCR)
- Key management and encryption-at-rest boundary

## What was not in scope

- Customer-managed integrations and customer-side configuration
- Third-party SaaS providers (their own assessments apply)
- Physical / personnel security (covered separately by SOC 2)

## Remediation policy

- Critical and High findings: remediated before any release impacted
  by the finding ships to customers.
- Medium findings: remediated within 30 days.
- Low / Informational: remediated within 90 days, or formally
  risk-accepted by the CTO and security lead.

All remediations are verified by the same vendor that produced the
original report.

## Ongoing assurance

- Annual third-party penetration test, scope refreshed each year
- Continuous vulnerability scanning across container images and Go /
  npm dependencies (`gosec`, `govulncheck`, `npm audit`,
  Snyk / equivalent)
- Quarterly internal threat-model review

## Contact

Security disclosure: security@&lt;company&gt;
Procurement security questionnaires: trust@&lt;company&gt;

---

*Last updated: YYYY-MM-DD. Supersedes any prior version with an
earlier date.*
