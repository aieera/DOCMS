import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import Papa from 'papaparse'
import { Upload, Check, X, AlertCircle } from 'lucide-react'
import { toast } from 'sonner'

import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Badge } from '@/components/ui/shadcn/badge'
import { inviteUser } from '@/api/admin'
import { readErrorMessage } from '@/api/client'

// ADR / Phase 4 — admin CSV bulk invite. Per-row loop over the existing
// /admin/users/invite endpoint (no batch endpoint on the backend; the
// auth service runs each invite inside its own tx + outbox event, so
// looping is the correct shape — there's nothing to batch atomically).
//
// We parse client-side with papaparse, validate each row (email format,
// role membership), and let the admin REVIEW + remove bad rows before
// any invites land. Account creation stays explicit: this is invitations
// only, the recipient sets their own password through the activation
// link, just like the single-invite flow.

const ALLOWED_ROLES = ['member', 'admin', 'viewer', 'compliance_officer'] as const
type AllowedRole = (typeof ALLOWED_ROLES)[number]
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

interface ParsedRow {
  index: number
  name: string
  email: string
  role: string
  errors: string[]
}

interface SendResult {
  email: string
  ok: boolean
  reason?: string
}

interface Props {
  open: boolean
  onOpenChange: (o: boolean) => void
}

function validateRow(raw: Record<string, string>, index: number): ParsedRow {
  // Header normalization: trim + lowercase so "Name" / "EMAIL" / "Role"
  // all map cleanly. We accept lower-snake aliases too for users who
  // export from spreadsheets that munge headers.
  const lookup = (...keys: string[]) => {
    for (const k of keys) {
      const v = raw[k] ?? raw[k.toLowerCase()] ?? raw[k.toUpperCase()]
      if (v !== undefined && v !== null) return String(v).trim()
    }
    return ''
  }
  const name = lookup('name', 'display_name', 'displayname', 'full name')
  const email = lookup('email', 'e-mail', 'mail').toLowerCase()
  const role = lookup('role').toLowerCase() || 'member'

  const errors: string[] = []
  if (!email) errors.push('email is required')
  else if (!EMAIL_RE.test(email)) errors.push('not a valid email address')
  if (!ALLOWED_ROLES.includes(role as AllowedRole)) {
    errors.push(`role must be one of ${ALLOWED_ROLES.join(', ')}`)
  }
  if (!name) {
    // We tolerate a missing display name — the single-invite UI also
    // falls back to email-as-name. Surface as a soft warning, not an
    // error, by leaving the errors array empty for this case.
  }
  return { index, name, email, role, errors }
}

export function BulkInviteDialog({ open, onOpenChange }: Props) {
  const qc = useQueryClient()
  const [rows, setRows] = useState<ParsedRow[]>([])
  const [parseError, setParseError] = useState<string | null>(null)
  const [sending, setSending] = useState(false)
  const [results, setResults] = useState<SendResult[] | null>(null)

  const reset = () => { setRows([]); setParseError(null); setResults(null) }
  const close = (o: boolean) => { onOpenChange(o); if (!o) reset() }

  const onFile = (file: File) => {
    setParseError(null)
    setResults(null)
    Papa.parse<Record<string, string>>(file, {
      header: true,
      skipEmptyLines: true,
      // transformHeader trims + lowercases so the validator can use
      // canonical keys without knowing whether the user uploaded
      // "Name,Email,Role" or "name,email,role".
      transformHeader: (h) => h.trim().toLowerCase(),
      complete: (res) => {
        if (res.errors.length > 0) {
          setParseError(`CSV parse failed: ${res.errors[0].message}`)
          return
        }
        const headers = (res.meta.fields ?? []).map((f) => f.toLowerCase())
        if (!headers.includes('email')) {
          setParseError('CSV must include an "email" column.')
          return
        }
        const parsed = (res.data as Record<string, string>[])
          .map((r, i) => validateRow(r, i + 1))
        setRows(parsed)
      },
      error: (err) => setParseError(err.message),
    })
  }

  const validRows = rows.filter((r) => r.errors.length === 0)
  const invalidCount = rows.length - validRows.length

  const sendAll = async () => {
    if (validRows.length === 0 || sending) return
    setSending(true)
    setResults(null)
    // Per-row sequential loop. The auth service rate-limits invites
    // per-tenant; firing N requests in parallel would defeat that and
    // also degrade the "report row N failed" UX. Slow but correct.
    const out: SendResult[] = []
    for (const r of validRows) {
      try {
        await inviteUser(r.email, r.role, r.name || r.email)
        out.push({ email: r.email, ok: true })
      } catch (err) {
        out.push({ email: r.email, ok: false, reason: readErrorMessage(err) ?? 'invite failed' })
      }
    }
    setResults(out)
    setSending(false)
    qc.invalidateQueries({ queryKey: ['admin', 'users'] })
    const okCount = out.filter((r) => r.ok).length
    if (okCount === out.length) {
      toast.success(`Sent ${okCount} invitations`)
    } else {
      toast.error(`Sent ${okCount} / ${out.length} — see the table for failures`)
    }
  }

  return (
    <Dialog open={open} onOpenChange={close} title="Bulk invite from CSV" size="xl">
      <div className="space-y-4">
        <div className="rounded-md border border-dashed border-border bg-muted/30 p-4 text-sm text-muted-foreground">
          CSV with a header row: <code className="rounded bg-muted px-1.5 py-0.5 font-mono">name,email,role</code>.
          Roles: {ALLOWED_ROLES.join(', ')}. Tenant <strong>owner</strong> grants happen one-by-one through the user dropdown — not via this flow.
        </div>

        <div>
          <label
            htmlFor="csv-file-input"
            className="inline-flex cursor-pointer items-center gap-2 rounded-md border border-border bg-background px-3 py-1.5 text-sm font-medium hover:bg-accent"
          >
            <Upload className="h-4 w-4" />
            Choose CSV…
          </label>
          <input
            id="csv-file-input"
            type="file"
            accept=".csv,text/csv"
            className="sr-only"
            data-testid="bulk-invite-file"
            onChange={(e) => {
              const f = e.target.files?.[0]
              if (f) onFile(f)
              e.target.value = ''
            }}
          />
          {parseError && (
            <p
              className="mt-2 flex items-center gap-1 text-sm text-destructive"
              data-testid="bulk-invite-parse-error"
            >
              <AlertCircle className="h-4 w-4" /> {parseError}
            </p>
          )}
        </div>

        {rows.length > 0 && (
          <div>
            <div className="mb-2 flex items-center gap-3 text-xs text-muted-foreground">
              <span>{validRows.length} valid</span>
              {invalidCount > 0 && (
                <span className="text-destructive">{invalidCount} with errors</span>
              )}
            </div>
            <div className="max-h-80 overflow-y-auto rounded-md border border-border">
              <table className="w-full text-sm">
                <thead className="bg-muted/40">
                  <tr>
                    <th className="px-3 py-2 text-start">#</th>
                    <th className="px-3 py-2 text-start">Name</th>
                    <th className="px-3 py-2 text-start">Email</th>
                    <th className="px-3 py-2 text-start">Role</th>
                    <th className="px-3 py-2 text-start">Status</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((r) => {
                    const result = results?.find((x) => x.email === r.email)
                    return (
                      <tr key={r.index} className="border-t border-border" data-testid={`bulk-invite-row-${r.index}`}>
                        <td className="px-3 py-2 align-top text-xs text-muted-foreground">{r.index}</td>
                        <td className="px-3 py-2 align-top">{r.name || <span className="text-muted-foreground">—</span>}</td>
                        <td className="px-3 py-2 align-top font-mono text-xs">{r.email}</td>
                        <td className="px-3 py-2 align-top"><Badge>{r.role}</Badge></td>
                        <td className="px-3 py-2 align-top text-xs">
                          {r.errors.length > 0 ? (
                            <span className="flex items-center gap-1 text-destructive">
                              <X className="h-3 w-3" /> {r.errors.join('; ')}
                            </span>
                          ) : result ? (
                            result.ok ? (
                              <span className="flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
                                <Check className="h-3 w-3" /> sent
                              </span>
                            ) : (
                              <span className="flex items-center gap-1 text-destructive">
                                <X className="h-3 w-3" /> {result.reason}
                              </span>
                            )
                          ) : (
                            <span className="text-muted-foreground">ready</span>
                          )}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          </div>
        )}

        <div className="flex items-center justify-end gap-2">
          <Button type="button" variant="ghost" onClick={() => close(false)} disabled={sending}>
            {results ? 'Close' : 'Cancel'}
          </Button>
          <Button
            type="button"
            onClick={sendAll}
            disabled={validRows.length === 0 || sending || !!results}
            loading={sending}
            data-testid="bulk-invite-send"
          >
            {results
              ? 'Done'
              : `Send ${validRows.length} invitation${validRows.length === 1 ? '' : 's'}`}
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
