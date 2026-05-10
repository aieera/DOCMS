import { FileIcon } from '@/components/ui/FileIcon'
import { Button } from '@/components/ui/shadcn/button'
import { Download } from 'lucide-react'

export function UnsupportedFormat({ url, mimeType }: { url: string; mimeType: string }) {
  return (
    <div className="flex flex-col items-center gap-4 py-16">
      <FileIcon mime={mimeType} className="h-16 w-16" />
      <p className="text-sm text-[var(--color-text-secondary)]">Preview not available for this file type</p>
      <Button variant="outline" onClick={() => window.open(url)}><Download className="h-4 w-4" /> Download</Button>
    </div>
  )
}
