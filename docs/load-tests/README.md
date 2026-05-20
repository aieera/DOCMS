# Load test campaign archive (ADR 0105)

Each subdirectory is one §16 validation campaign. Format:
`YYYY-MM-DD/summary.md`.

The summary file is the buyer-facing artifact. Every column on the
"Verdict per §16 SLO" table must be filled in before a campaign is
considered shipped — partial summaries are NOT acceptable.

To start a new campaign, follow `RUNBOOK.md`. The output of step 4
of the runbook is the populated summary file in this directory.

The frontend's `/admin/platform/load-tests` page glob-imports every
`*/summary.md` here at vite build time and renders them in
reverse-chronological order. No backend endpoint is involved — the
repo IS the report database.
