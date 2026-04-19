import { createFileRoute } from '@tanstack/react-router'
import { FileIcon } from '@/components/ui/FileIcon'
import { Button } from '@/components/ui/Button'
import { Download, Lock } from 'lucide-react'
import { useState } from 'react'
import { Input } from '@/components/ui/Input'

function SharedViewerPage() {
  const { token } = Route.useParams()
  const [password, setPassword] = useState('')
  const [needsPassword] = useState(false)
  const [unlocked, setUnlocked] = useState(false)

  if (needsPassword && !unlocked) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[var(--color-bg)]">
        <div className="w-full max-w-sm space-y-4 rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 shadow-lg">
          <div className="flex flex-col items-center gap-2">
            <Lock className="h-8 w-8 text-[var(--color-primary)]" />
            <h2 className="text-lg font-semibold">Password Required</h2>
          </div>
          <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="Enter password" />
          <Button className="w-full" onClick={() => setUnlocked(true)}>Unlock</Button>
        </div>
      </div>
    )
  }

  return (
    <div className="flex min-h-screen flex-col items-center justify-center bg-[var(--color-bg)] p-8">
      <div className="w-full max-w-2xl rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-8 shadow-lg">
        <div className="mb-6 flex items-center justify-between">
          <div className="flex items-center gap-3">
            <FileIcon mime="application/pdf" className="h-8 w-8" />
            <div>
              <h1 className="text-lg font-semibold">Shared Document</h1>
              <p className="text-sm text-[var(--color-text-secondary)]">Share token: {token}</p>
            </div>
          </div>
          <Button variant="outline"><Download className="h-4 w-4" /> Download</Button>
        </div>
        <div className="flex items-center justify-center rounded-lg bg-slate-50 py-20 dark:bg-slate-800">
          <p className="text-sm text-[var(--color-text-secondary)]">Document preview loads here</p>
        </div>
        <p className="mt-4 text-center text-xs text-[var(--color-text-secondary)]">Shared via VaultDMS</p>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/shared/$token')({ component: SharedViewerPage })
