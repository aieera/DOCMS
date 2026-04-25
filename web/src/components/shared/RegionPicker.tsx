import { AlertTriangle } from 'lucide-react'
import { REGIONS } from '@/lib/regions'

interface Props {
  value: string
  onChange: (region: string) => void
  // Optional org-wide allowlist. When set, regions outside the list
  // are still selectable but produce a warning under the dropdown so
  // the user can self-correct before submitting (the backend rejects
  // either way; the warning is only there to spare the round trip).
  allowedRegions?: string[]
  id?: string
  disabled?: boolean
  testId?: string
}

export function RegionPicker({ value, onChange, allowedRegions, id, disabled, testId }: Props) {
  const allowed = allowedRegions && allowedRegions.length > 0 ? new Set(allowedRegions) : null
  const outsideAllowlist = allowed !== null && !allowed.has(value)

  return (
    <div>
      <select
        id={id}
        data-testid={testId}
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
        className="w-full rounded border border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-2 py-1.5 text-sm"
      >
        {REGIONS.map((r) => {
          const blocked = allowed !== null && !allowed.has(r.code)
          return (
            <option key={r.code} value={r.code}>
              {r.displayName} · {r.boundary}
              {blocked ? ' (outside allowlist)' : ''}
            </option>
          )
        })}
      </select>
      {outsideAllowlist && (
        <p
          role="alert"
          data-testid={testId ? `${testId}-warning` : undefined}
          className="mt-1 flex items-start gap-1 text-xs text-amber-700 dark:text-amber-300"
        >
          <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" aria-hidden="true" />
          This region is outside your organization's allowed regions and will be rejected on save.
        </p>
      )}
    </div>
  )
}
