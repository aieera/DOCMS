import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useEffect } from 'react'

import { listProfiles, profileImageURL, type SignatureProfile } from '@/api/signatureProfiles'

/**
 * Wave 15.4 — signature profile picker.
 *
 * Drop into any sign / envelope surface. On mount it fetches the
 * caller's profiles and pre-selects the default (brief DoD: "default
 * prefills; user can switch"). The server NEVER trusts a client
 * image — downstream callers pass the returned `profileId` to the
 * backend, which re-fetches + decrypts the bytes. This component's
 * img tag is purely visual and lives on the same session-auth'd
 * connection as the rest of the app.
 *
 * When the user has no saved profiles, the picker renders a "Draw
 * new" nudge pointing at /settings/signatures so they can create
 * one before signing.
 */
export function SignatureProfilePicker({
  value,
  onChange,
}: {
  value: string | null
  onChange: (profileId: string | null) => void
}) {
  const { data, isLoading } = useQuery({
    queryKey: ['signature-profiles'],
    queryFn: listProfiles,
  })

  // Pre-select the default the first time the list arrives.
  useEffect(() => {
    if (value !== null || !data) return
    const def = data.find((p) => p.is_default) ?? data[0]
    if (def) onChange(def.id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data])

  if (isLoading) {
    return (
      <p className="text-sm text-[var(--color-text-secondary)]" role="status" aria-live="polite">
        Loading your saved signatures…
      </p>
    )
  }

  if (!data || data.length === 0) {
    return (
      <div className="rounded-md border border-dashed border-[var(--color-border)] p-3 text-sm">
        <p className="text-[var(--color-text-secondary)]">
          No saved signatures yet.
        </p>
        <Link
          to="/settings/signatures"
          className="text-[var(--color-primary)] hover:underline"
        >
          Create one in Settings → Saved signatures
        </Link>
      </div>
    )
  }

  return (
    <fieldset className="space-y-2" aria-label="Choose a signature">
      <legend className="mb-1 text-sm font-medium">Signature</legend>
      <div className="grid grid-cols-2 gap-2">
        {data.map((p) => (
          <ProfileOption
            key={p.id}
            profile={p}
            selected={value === p.id}
            onSelect={() => onChange(p.id)}
          />
        ))}
      </div>
    </fieldset>
  )
}

function ProfileOption({
  profile,
  selected,
  onSelect,
}: {
  profile: SignatureProfile
  selected: boolean
  onSelect: () => void
}) {
  return (
    <label
      className={
        'flex cursor-pointer items-center gap-3 rounded-md border p-2 ' +
        (selected
          ? 'border-[var(--color-primary)] ring-2 ring-[var(--color-primary)]'
          : 'border-[var(--color-border)] hover:bg-slate-50 dark:hover:bg-slate-800')
      }
    >
      <input
        type="radio"
        name="signature-profile"
        checked={selected}
        onChange={onSelect}
        className="sr-only"
      />
      <img
        src={profileImageURL(profile.id)}
        alt={`Signature for ${profile.name}`}
        className="h-12 w-28 rounded border border-[var(--color-border)] bg-white object-contain p-1"
      />
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium">{profile.name}</p>
        {profile.is_default && (
          <p className="text-xs text-[var(--color-primary)]">Default</p>
        )}
      </div>
    </label>
  )
}
