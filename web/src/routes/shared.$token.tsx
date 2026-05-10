import { createFileRoute } from '@tanstack/react-router'
import { FileIcon } from '@/components/ui/FileIcon'
import { Button } from '@/components/ui/shadcn/button'
import { Download, Lock } from 'lucide-react'
import { useState } from 'react'
import { Input } from '@/components/ui/shadcn/input'

function SharedViewerPage() {
  const { token } = Route.useParams()
  const [password, setPassword] = useState('')
  const [needsPassword] = useState(false)
  const [unlocked, setUnlocked] = useState(false)

  if (needsPassword && !unlocked) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <div className="w-full max-w-sm space-y-4 rounded-xl border border-border bg-card p-8 shadow-lg">
          <div className="flex flex-col items-center gap-2">
            <Lock className="h-8 w-8 text-primary" />
            <h2 className="text-lg font-semibold">Password Required</h2>
          </div>
          <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="Enter password" />
          <Button className="w-full" onClick={() => setUnlocked(true)}>Unlock</Button>
        </div>
      </div>
    )
  }

  return (
    <div className="flex min-h-screen flex-col items-center justify-center bg-background p-8">
      <div className="w-full max-w-2xl rounded-xl border border-border bg-card p-8 shadow-lg">
        <div className="mb-6 flex items-center justify-between">
          <div className="flex items-center gap-3">
            <FileIcon mime="application/pdf" className="h-8 w-8" />
            <div>
              <h1 className="text-lg font-semibold">Shared Document</h1>
              <p className="text-sm text-muted-foreground">Share token: {token}</p>
            </div>
          </div>
          <Button variant="outline"><Download className="h-4 w-4" /> Download</Button>
        </div>
        <div className="flex items-center justify-center rounded-lg bg-muted/40 py-20">
          <p className="text-sm text-muted-foreground">Document preview loads here</p>
        </div>
        <p className="mt-4 text-center text-xs text-muted-foreground">Shared via VaultDMS</p>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/shared/$token')({ component: SharedViewerPage })
