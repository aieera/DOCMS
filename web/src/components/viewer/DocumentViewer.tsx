import { useState } from 'react'
import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { PDFViewer } from './PDFViewer'
import { ImageViewer } from './ImageViewer'
import { TextViewer } from './TextViewer'
import { VideoPlayer } from './VideoPlayer'
import { UnsupportedFormat } from './UnsupportedFormat'
import { Download, Share, ZoomIn, ZoomOut, RotateCw } from 'lucide-react'

interface Props { open: boolean; onClose: () => void; title: string; mimeType: string; url: string }

export function DocumentViewer({ open, onClose, title, mimeType, url }: Props) {
  const [zoom, setZoom] = useState(100)
  const [rotation, setRotation] = useState(0)

  const Viewer = mimeType === 'application/pdf' ? PDFViewer
    : mimeType.startsWith('image/') ? ImageViewer
    : mimeType.startsWith('video/') ? VideoPlayer
    : mimeType.startsWith('text/') ? TextViewer
    : UnsupportedFormat

  return (
    <Dialog open={open} onOpenChange={onClose} title={title} size="full">
      <div className="flex items-center justify-between border-b border-[var(--color-border)] pb-3">
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="sm" aria-label="Zoom out" onClick={() => setZoom((z) => Math.max(25, z - 25))}><ZoomOut className="h-4 w-4" /></Button>
          <span className="w-12 text-center text-xs">{zoom}%</span>
          <Button variant="ghost" size="sm" aria-label="Zoom in" onClick={() => setZoom((z) => Math.min(400, z + 25))}><ZoomIn className="h-4 w-4" /></Button>
          <Button variant="ghost" size="sm" aria-label="Rotate 90 degrees" onClick={() => setRotation((r) => (r + 90) % 360)}><RotateCw className="h-4 w-4" /></Button>
        </div>
        <div className="flex items-center gap-1">
          <Button variant="ghost" size="sm" aria-label="Download document"><Download className="h-4 w-4" /></Button>
          <Button variant="ghost" size="sm" aria-label="Share document"><Share className="h-4 w-4" /></Button>
        </div>
      </div>
      <div className="mt-3 flex-1 overflow-auto" style={{ transform: `scale(${zoom / 100}) rotate(${rotation}deg)`, transformOrigin: 'top center' }}>
        <Viewer url={url} mimeType={mimeType} />
      </div>
    </Dialog>
  )
}
