import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { FileLock2, ShieldCheck } from 'lucide-react'

import { Star, Snowflake } from 'lucide-react'

import { getRecordForDocument, listCategories, declareRecord, setVital, freezeRecord, unfreezeRecord, type RecordRow } from '@/api/records'
import { useAppMutation } from '@/hooks/useAppMutation'
import { Button } from '@/components/ui/shadcn/button'

// "Declare as record" action for the document detail. Shows the current record
// status when already declared, or a category picker (series nodes) to declare
// otherwise. Declaring freezes the document (immutable until disposition).
export function DeclareRecordButton({ documentId, canManage }: { documentId: string; canManage: boolean }) {
  const qc = useQueryClient()
  const { data: record, isLoading } = useQuery({
    queryKey: ['record', documentId],
    queryFn: () => getRecordForDocument(documentId),
  })
  const { data: categories = [], isLoading: catsLoading } = useQuery({
    queryKey: ['record-categories'],
    queryFn: listCategories,
    enabled: canManage && !record,
  })
  const series = categories.filter((c) => c.node_type === 'series')
  const [picking, setPicking] = useState(false)
  const [catId, setCatId] = useState('')

  const declare = useAppMutation({
    mutationFn: () => declareRecord(documentId, catId),
    onSuccess: () => {
      toast.success('Declared as record — document is now immutable until disposition')
      setPicking(false)
      qc.invalidateQueries({ queryKey: ['record', documentId] })
    },
    defaultErrorMessage: 'Could not declare record',
  })

  if (isLoading) return null

  if (record) {
    const disposed = record.disposition_state === 'disposed' || record.disposition_state === 'transferred'
    return <RecordStatus record={record} documentId={documentId} canManage={canManage} disposed={disposed} />
  }

  if (!canManage) {
    return <span className="text-xs text-muted-foreground" data-testid="record-none">Not declared as a record.</span>
  }

  if (!picking) {
    return (
      <Button size="sm" variant="outline" onClick={() => setPicking(true)} data-testid="declare-record">
        <FileLock2 className="h-3.5 w-3.5" /> Declare as record
      </Button>
    )
  }

  if (catsLoading) {
    return <span className="text-xs text-muted-foreground" data-testid="declare-record-loading">Loading record series…</span>
  }

  // No file plan configured yet — an empty <select> looks broken, so guide the
  // admin to build the file plan (retention schedules + at least one series)
  // before records can be declared. Plain <a> so it works without a router in
  // isolation; it's a rare cross-area jump.
  if (series.length === 0) {
    return (
      <div className="flex flex-col gap-1 rounded-md border border-border bg-muted/30 px-3 py-2 text-xs" data-testid="declare-record-empty">
        <span className="font-medium">No record series configured</span>
        <span className="text-muted-foreground">
          Set up a file plan (retention schedules + a series) in{' '}
          <a href="/admin/records-retention?tab=records" className="text-primary underline underline-offset-2">
            Admin → Records
          </a>{' '}
          before declaring records.
        </span>
        <button type="button" className="mt-1 self-start text-muted-foreground hover:text-foreground" onClick={() => setPicking(false)}>
          Cancel
        </button>
      </div>
    )
  }

  return (
    <div className="flex items-center gap-2" data-testid="declare-record-picker">
      <select
        className="rounded border border-border bg-background px-2 py-1 text-sm"
        value={catId}
        onChange={(e) => setCatId(e.target.value)}
      >
        <option value="">Pick a series…</option>
        {series.map((c) => (
          <option key={c.id} value={c.id}>{c.code ? `${c.code} — ` : ''}{c.name}</option>
        ))}
      </select>
      <Button size="sm" disabled={!catId || declare.isPending} onClick={() => declare.mutate(undefined)} data-testid="declare-confirm">
        <ShieldCheck className="h-3.5 w-3.5" /> Declare
      </Button>
      <Button size="sm" variant="ghost" onClick={() => setPicking(false)}>Cancel</Button>
    </div>
  )
}

// RecordStatus shows a declared record's state plus the vital + freeze
// controls (DoD 5015.2 / ISO 15489 mechanisms). Hidden once disposed.
function RecordStatus({ record, documentId, canManage, disposed }: {
  record: RecordRow; documentId: string; canManage: boolean; disposed: boolean
}) {
  const qc = useQueryClient()
  const invalidate = () => qc.invalidateQueries({ queryKey: ['record', documentId] })

  const vital = useAppMutation({
    mutationFn: () => setVital(record.id, !record.vital_record),
    onSuccess: () => { toast.success(record.vital_record ? 'Vital flag cleared' : 'Marked vital'); invalidate() },
    defaultErrorMessage: 'Could not update vital flag',
  })
  const freeze = useAppMutation({
    mutationFn: () => record.frozen ? unfreezeRecord(record.id) : freezeRecord(record.id, 'records freeze'),
    onSuccess: () => { toast.success(record.frozen ? 'Unfrozen' : 'Frozen — disposition halted'); invalidate() },
    defaultErrorMessage: 'Could not change freeze state',
  })

  return (
    <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted/30 px-2 py-1.5 text-xs" data-testid="record-status">
      <FileLock2 className={`h-3.5 w-3.5 ${disposed ? 'text-muted-foreground' : 'text-violet-500'}`} />
      <span className="font-medium">Record</span>
      <span className="rounded bg-muted px-1.5 py-0.5">{record.disposition_state}</span>
      {record.vital_record && <span className="inline-flex items-center gap-0.5 text-amber-600"><Star className="h-3 w-3 fill-current" /> vital</span>}
      {record.frozen && <span className="inline-flex items-center gap-0.5 text-sky-600"><Snowflake className="h-3 w-3" /> frozen</span>}
      {record.cutoff_date && <span className="text-muted-foreground">cutoff {new Date(record.cutoff_date).toLocaleDateString()}</span>}
      {canManage && !disposed && (
        <span className="ms-auto flex items-center gap-1">
          <Button size="sm" variant="ghost" className="h-6 px-1.5" disabled={vital.isPending}
            onClick={() => vital.mutate(undefined)} data-testid="toggle-vital" title={record.vital_record ? 'Clear vital' : 'Mark vital'}>
            <Star className={`h-3.5 w-3.5 ${record.vital_record ? 'fill-current text-amber-500' : ''}`} />
          </Button>
          <Button size="sm" variant="ghost" className="h-6 px-1.5" disabled={freeze.isPending}
            onClick={() => freeze.mutate(undefined)} data-testid="toggle-freeze" title={record.frozen ? 'Unfreeze' : 'Freeze'}>
            <Snowflake className={`h-3.5 w-3.5 ${record.frozen ? 'text-sky-500' : ''}`} />
          </Button>
        </span>
      )}
    </div>
  )
}
