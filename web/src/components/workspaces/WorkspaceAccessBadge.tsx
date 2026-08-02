import { Lock, Users } from 'lucide-react'

import { Badge } from '@/components/ui/shadcn/badge'
import { cn } from '@/lib/cn'

// Private vs shared, at a glance.
//
// Whether other people can see a workspace is *state*, not another number,
// so it gets a pill rather than a fourth muted counter in the meta row —
// state you must notice shouldn't be typographically identical to a
// document count you merely read.
//
// The two states are weighted differently on purpose. Private is the quiet
// default: an outline pill that recedes. Shared is filled, because it is the
// one that changes what you can safely put in the workspace — but it stays
// on the neutral `secondary` token rather than a semantic warning colour,
// since sharing is a normal thing to do, not a fault.
//
// Reads member_count only as a fallback for older API responses that predate
// shared_with_count: there, "1 member" is the auto-enrolled creator, so
// anything above 1 means shared.
export function WorkspaceAccessBadge({
  sharedWithCount,
  memberCount,
  className,
}: {
  sharedWithCount?: number | string
  memberCount?: number | string
  className?: string
}) {
  // protobuf int64 marshals to a JSON *string*, so these arrive as "3", not
  // 3. Coerce rather than compare loosely — the identical assumption in the
  // meta row (`member_count === 1 ? 'member' : 'members'`) is why every card
  // used to read "1 members".
  const toCount = (v: number | string | undefined) => {
    const n = Number(v)
    return Number.isFinite(n) ? n : undefined
  }
  const explicit = toCount(sharedWithCount)
  const shared = explicit ?? Math.max(0, (toCount(memberCount) ?? 1) - 1)

  if (shared <= 0) {
    return (
      <Badge
        variant="outline"
        className={cn('gap-1 font-normal text-muted-foreground', className)}
        title="Only you can open this workspace. Share it to give someone else access."
      >
        <Lock className="h-3 w-3" aria-hidden />
        Private
      </Badge>
    )
  }

  return (
    <Badge
      variant="secondary"
      className={cn('gap-1 font-normal', className)}
      title={`${shared} ${shared === 1 ? 'person has' : 'people have'} been given access to this workspace.`}
    >
      <Users className="h-3 w-3" aria-hidden />
      {/* The number is the useful part — "Shared" alone doesn't tell you
          whether that means one colleague or the whole company. */}
      Shared · <span className="tabular-nums">{shared}</span>
    </Badge>
  )
}
