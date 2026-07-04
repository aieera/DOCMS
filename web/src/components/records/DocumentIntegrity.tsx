import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ShieldCheck, ShieldAlert, ShieldQuestion, Lock, Spline } from 'lucide-react'

import { getDocumentMerkleProof, getWormStatus, wormLock, type MerkleProof } from '@/api/integrity'
import { useAppMutation } from '@/hooks/useAppMutation'
import { Button } from '@/components/ui/shadcn/button'
import { Spinner } from '@/components/ui/Spinner'

// Per-document audit integrity (Merkle/hash-chain proof) + WORM object-lock.
// "Verify integrity" computes a proof over the document's audit trail and
// shows a verified/tampered badge with the root hash + range; the WORM
// indicator shows object-lock retention, with an admin lock action.
function short(h?: string) {
  return h ? `${h.slice(0, 8)}…${h.slice(-6)}` : '—'
}

export function DocumentIntegrity({ documentId, canManage }: { documentId: string; canManage: boolean }) {
  const qc = useQueryClient()
  const { data: worm } = useQuery({ queryKey: ['worm', documentId], queryFn: () => getWormStatus(documentId) })
  const [proof, setProof] = useState<MerkleProof | null>(null)
  const [lockOpen, setLockOpen] = useState(false)
  const [until, setUntil] = useState('')

  const verify = useAppMutation({
    mutationFn: () => getDocumentMerkleProof(documentId),
    onSuccess: (p: MerkleProof) => {
      setProof(p)
      if (p.valid) toast.success('Audit trail verified — proof is intact')
      else toast.error('Tampering detected in the audit trail')
    },
    defaultErrorMessage: 'Could not verify integrity',
  })

  const lock = useAppMutation({
    mutationFn: () => wormLock(documentId, new Date(until + 'T00:00:00Z').toISOString()),
    onSuccess: () => {
      toast.success('WORM object-lock applied')
      setLockOpen(false)
      qc.invalidateQueries({ queryKey: ['worm', documentId] })
    },
    defaultErrorMessage: 'Could not apply WORM lock',
  })

  return (
    <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted/20 px-2 py-1.5 text-xs" data-testid="document-integrity">
      <Button size="sm" variant="outline" className="h-7" disabled={verify.isPending}
        onClick={() => verify.mutate(undefined)} data-testid="verify-integrity">
        {verify.isPending ? <Spinner className="h-3 w-3" /> : <Spline className="h-3.5 w-3.5" />}
        Verify integrity
      </Button>

      {/* integrity badge + proof detail */}
      {proof && (proof.valid ? (
        <span className="inline-flex items-center gap-1 rounded bg-emerald-500/10 px-1.5 py-0.5 text-emerald-600" data-testid="badge-verified">
          <ShieldCheck className="h-3.5 w-3.5" /> Verified
        </span>
      ) : (
        <span className="inline-flex items-center gap-1 rounded bg-red-500/10 px-1.5 py-0.5 text-red-600" data-testid="badge-tampered">
          <ShieldAlert className="h-3.5 w-3.5" /> Tampered
        </span>
      ))}
      {proof && (
        <span className="text-muted-foreground" title={`root ${proof.root_hash}\nbroken_at ${proof.broken_at ?? '—'}`}>
          root {short(proof.root_hash)} · {proof.count} event{proof.count === 1 ? '' : 's'}
        </span>
      )}
      {!proof && (
        <span className="inline-flex items-center gap-1 text-muted-foreground"><ShieldQuestion className="h-3.5 w-3.5" /> not verified</span>
      )}

      {/* WORM indicator + admin lock */}
      {worm?.locked && (
        <span className="ms-auto inline-flex items-center gap-1 rounded bg-sky-500/10 px-1.5 py-0.5 text-sky-600" data-testid="worm-indicator">
          <Lock className="h-3.5 w-3.5" /> WORM until {worm.worm_retain_until ? new Date(worm.worm_retain_until).toLocaleDateString() : ''}
        </span>
      )}
      {canManage && !worm?.locked && (
        lockOpen ? (
          <span className="ms-auto inline-flex items-center gap-1">
            <input type="date" className="rounded border border-border bg-background px-1.5 py-0.5 text-xs"
              value={until} onChange={(e) => setUntil(e.target.value)} data-testid="worm-until" />
            <Button size="sm" className="h-7" disabled={!until || lock.isPending} onClick={() => lock.mutate(undefined)} data-testid="worm-apply">
              <Lock className="h-3.5 w-3.5" /> Lock
            </Button>
            <Button size="sm" variant="ghost" className="h-7" onClick={() => setLockOpen(false)}>Cancel</Button>
          </span>
        ) : (
          <Button size="sm" variant="ghost" className="ms-auto h-7" onClick={() => setLockOpen(true)} data-testid="worm-lock-start">
            <Lock className="h-3.5 w-3.5" /> WORM lock
          </Button>
        )
      )}
    </div>
  )
}
