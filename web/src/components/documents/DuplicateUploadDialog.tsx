import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { useUploadStore } from '@/store/uploadStore'
import { formatRelativeTime } from '@/lib/formatters'

// DuplicateUploadDialog renders the pending "possible duplicate" prompt
// from the upload store and resolves the upload flow's awaited decision.
// Mounted once globally (in the authenticated layout) so no upload call
// site has to wire it. Cancel = skip this file; Upload anyway = proceed
// (a new document row is created even though the content already exists).
export function DuplicateUploadDialog() {
  const prompt = useUploadStore((s) => s.duplicatePrompt)
  const resolveDuplicate = useUploadStore((s) => s.resolveDuplicate)

  const open = !!prompt
  const matches = prompt?.matches ?? []

  return (
    <Dialog
      open={open}
      // Closing via backdrop/escape is treated as "skip" — the safe
      // default that avoids silently creating a duplicate.
      onOpenChange={(o) => { if (!o) resolveDuplicate(false) }}
      title="Possible duplicate"
      description={
        prompt
          ? `"${prompt.fileName}" has the same content as ${matches.length} existing ${matches.length === 1 ? 'document' : 'documents'}.`
          : ''
      }
      size="md"
    >
      <div className="space-y-4" data-testid="duplicate-upload-dialog">
        <ul className="max-h-60 space-y-1 overflow-auto rounded-md border border-border bg-muted/20 p-2">
          {matches.map((m) => (
            <li key={m.document_id} className="flex items-center justify-between gap-2 px-1 py-1 text-sm">
              <span className="truncate">{m.title || 'Untitled document'}</span>
              <span className="shrink-0 text-xs text-muted-foreground">
                {formatRelativeTime(m.created_at)}
              </span>
            </li>
          ))}
        </ul>
        <p className="text-xs text-muted-foreground">
          Uploading anyway creates a new document that shares the existing stored content
          (no extra storage is used). Skip if this is a redundant copy.
        </p>
        <div className="flex justify-end gap-2">
          <Button
            variant="outline"
            onClick={() => resolveDuplicate(false)}
            data-testid="duplicate-skip"
          >
            Skip
          </Button>
          <Button
            onClick={() => resolveDuplicate(true)}
            data-testid="duplicate-proceed"
          >
            Upload anyway
          </Button>
        </div>
      </div>
    </Dialog>
  )
}
