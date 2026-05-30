import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Globe, Lock } from 'lucide-react'
import { cn } from '@/lib/cn'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Spinner } from '@/components/ui/Spinner'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/shadcn/dialog'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  // parentName drives the dialog subtitle: "New folder in HR" vs
  // "New folder in HR / Onboarding". null = workspace root.
  parentName: string | null
  // canPickVisibility — false in Phase 1, true in Phase 2 once the
  // backend supports private folders. When false the dialog only
  // creates shared folders.
  canPickVisibility?: boolean
  onCreate: (name: string, visibility: 'shared' | 'private') => Promise<void> | void
  isCreating: boolean
}

// NewFolderDialog is the modal triggered by the "New folder" button
// in the workspace toolbar. Single text field + (optional) shared/
// private toggle. Enter submits.
export function NewFolderDialog({
  open,
  onOpenChange,
  parentName,
  canPickVisibility = false,
  onCreate,
  isCreating,
}: Props) {
  const { t } = useTranslation('folders')
  const [name, setName] = useState('')
  const [visibility, setVisibility] = useState<'shared' | 'private'>('shared')
  const trimmed = name.trim()
  const canSubmit = trimmed.length > 0 && !isCreating

  const handleClose = (next: boolean) => {
    onOpenChange(next)
    if (!next) {
      setName('')
      setVisibility('shared')
    }
  }

  const submit = async () => {
    if (!canSubmit) return
    await onCreate(trimmed, visibility)
    setName('')
    setVisibility('shared')
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('new_dialog.title')}</DialogTitle>
          <DialogDescription>
            {parentName
              ? t('new_dialog.description_in_folder', { name: parentName })
              : t('new_dialog.description_in_workspace')}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <div className="space-y-1">
            <label className="block text-xs font-medium text-muted-foreground">
              {t('new_dialog.name_label')}
            </label>
            <Input
              autoFocus
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t('new_dialog.name_placeholder')}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  void submit()
                }
              }}
              data-testid="new-folder-name"
            />
          </div>

          {canPickVisibility && (
            <div className="space-y-1">
              <label className="block text-xs font-medium text-muted-foreground">
                {t('new_dialog.visibility_label')}
              </label>
              <div className="grid grid-cols-2 gap-2">
                <VisibilityChoice
                  active={visibility === 'shared'}
                  onClick={() => setVisibility('shared')}
                  icon={<Globe className="h-4 w-4" />}
                  title={t('visibility.shared')}
                  hint={t('visibility.shared_hint')}
                  testId="new-folder-shared"
                />
                <VisibilityChoice
                  active={visibility === 'private'}
                  onClick={() => setVisibility('private')}
                  icon={<Lock className="h-4 w-4" />}
                  title={t('visibility.private')}
                  hint={t('visibility.private_hint')}
                  testId="new-folder-private"
                />
              </div>
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => handleClose(false)} disabled={isCreating}>
            {t('actions.cancel')}
          </Button>
          <Button onClick={submit} disabled={!canSubmit} data-testid="new-folder-create">
            {isCreating ? <Spinner className="me-1 h-3.5 w-3.5" /> : null}
            {t('actions.create')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function VisibilityChoice({
  active,
  onClick,
  icon,
  title,
  hint,
  testId,
}: {
  active: boolean
  onClick: () => void
  icon: React.ReactNode
  title: string
  hint: string
  testId?: string
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      data-testid={testId}
      className={cn(
        'flex flex-col items-start gap-1 rounded-xl border p-3 text-start transition-colors',
        active ? 'border-primary bg-primary/10' : 'border-border hover:border-primary/40',
      )}
    >
      <span className="flex items-center gap-1.5 text-sm font-medium">
        {icon}
        {title}
      </span>
      <span className="text-[10px] text-muted-foreground">{hint}</span>
    </button>
  )
}
