import { useState, useRef } from 'react'

import { useAuthBlob } from '@/lib/useAuthBlob'

export function ImageViewer({ url }: { url: string; mimeType?: string }) {
  const [dragging, setDragging] = useState(false)
  const [offset, setOffset] = useState({ x: 0, y: 0 })
  const start = useRef({ x: 0, y: 0 })
  const blobUrl = useAuthBlob(url)

  return (
    <div
      className="flex cursor-grab items-center justify-center overflow-hidden active:cursor-grabbing"
      onMouseDown={(e) => { setDragging(true); start.current = { x: e.clientX - offset.x, y: e.clientY - offset.y } }}
      onMouseMove={(e) => { if (dragging) setOffset({ x: e.clientX - start.current.x, y: e.clientY - start.current.y }) }}
      onMouseUp={() => setDragging(false)}
      onMouseLeave={() => setDragging(false)}
    >
      {blobUrl && (
        <img src={blobUrl} alt="Preview" className="max-h-[80vh] max-w-full object-contain" style={{ transform: `translate(${offset.x}px, ${offset.y}px)` }} draggable={false} />
      )}
    </div>
  )
}
