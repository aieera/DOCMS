import { useTranslation } from 'react-i18next'
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { FolderDown } from 'lucide-react'

import { importGoogleDrive, type DriveImportResult } from '@/api/connectors'
import { getWorkspaces, getFolders } from '@/api/workspaces'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'

// DriveImportPanel imports files from a Google Drive folder into a SeDoc
// workspace folder. Shown inside the Google connector modal once the
// tenant has authorized. The import runs as the current admin — their
// permissions gate the destination. Google-native editor docs
// (Docs/Sheets/Slides) are exported to PDF; folders + Forms/Sites are
// skipped. The backend processes synchronously and returns per-file
// counts, so this drives a single request with a spinner.
export function DriveImportPanel() {
  const { t } = useTranslation('common')
  const [driveFolderID, setDriveFolderID] = useState('')
  const [workspaceID, setWorkspaceID] = useState('')
  const [folderID, setFolderID] = useState('')
  const [result, setResult] = useState<DriveImportResult | null>(null)

  const workspacesQ = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces })
  const foldersQ = useQuery({
    queryKey: ['folders', workspaceID],
    queryFn: () => getFolders(workspaceID),
    enabled: workspaceID !== '',
  })

  const importMut = useAppMutation({
    mutationFn: () => importGoogleDrive({
      drive_folder_id: driveFolderID.trim() || undefined,
      workspace_id: workspaceID,
      folder_id: folderID,
    }),
    onSuccess: (res) => {
      setResult(res)
      if (res.failed > 0) {
        toast.warning(t('adm.cred.drive_partial', { imported: res.imported, skipped: res.skipped, failed: res.failed }))
      } else {
        toast.success(
          t('adm.cred.drive_ok', { count: res.imported })
          + (res.skipped ? t('adm.cred.drive_ok_skipped', { skipped: res.skipped }) : ''),
        )
      }
    },
    onError: (e: unknown) => {
      const detail = (e as { response?: { data?: { error?: string } } }).response?.data?.error
      toast.error(detail || t('adm.cred.drive_err'))
    },
  })

  const canImport = workspaceID !== '' && folderID !== '' && !importMut.isPending

  return (
    <div className="space-y-3 rounded-lg border border-border bg-muted/20 p-4">
      <div className="flex items-center gap-2">
        <FolderDown className="h-4 w-4 text-muted-foreground" />
        <h3 className="text-sm font-medium">{t('adm.cred.drive_title')}</h3>
      </div>
      <p className="text-xs text-muted-foreground">
        {t('adm.cred.drive_desc')}
      </p>

      <div>
        <label className="mb-1 block text-xs font-medium">{t('adm.cred.drive_folder')}</label>
        <Input
          value={driveFolderID}
          onChange={(e) => setDriveFolderID(e.target.value)}
          dir="ltr"
          placeholder={t('adm.cred.drive_folder_ph')}
          autoComplete="off"
          spellCheck={false}
        />
      </div>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <label className="mb-1 block text-xs font-medium">{t('adm.cred.workspace')}</label>
          <select
            className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
            value={workspaceID}
            onChange={(e) => { setWorkspaceID(e.target.value); setFolderID('') }}
          >
            <option value="">{t('adm.cred.select')}</option>
            {(workspacesQ.data ?? []).map((w) => (
              <option key={w.id} value={w.id}>{w.name}</option>
            ))}
          </select>
        </div>
        <div>
          <label className="mb-1 block text-xs font-medium">{t('adm.cred.folder')}</label>
          <select
            className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm disabled:opacity-50"
            value={folderID}
            onChange={(e) => setFolderID(e.target.value)}
            disabled={workspaceID === '' || foldersQ.isLoading}
          >
            <option value="">{t('adm.cred.select')}</option>
            {(foldersQ.data ?? []).map((f) => (
              <option key={f.id} value={f.id}>{f.name}</option>
            ))}
          </select>
        </div>
      </div>

      <div className="flex items-center justify-between">
        <Button size="sm" onClick={() => importMut.mutate()} disabled={!canImport}>
          {importMut.isPending ? t('adm.cred.drive_importing') : t('adm.cred.drive_import')}
        </Button>
        {result && (
          <span className="text-xs text-muted-foreground">
            {result.imported} imported · {result.skipped} skipped · {result.failed} failed
            {result.truncated ? ' · more remain (run again)' : ''}
          </span>
        )}
      </div>

      {result?.errors && result.errors.length > 0 && (
        <ul className="max-h-24 overflow-auto rounded border border-destructive/30 bg-destructive/5 p-2 text-xs text-foreground">
          {result.errors.slice(0, 10).map((err, i) => <li key={i}>{err}</li>)}
        </ul>
      )}
    </div>
  )
}
