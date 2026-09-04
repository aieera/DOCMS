// ADR 0067 — video annotation layer.
//
// Lets the viewer drop time-coded comment pins on the playback
// timeline. Shape persisted as { at_seconds, body }; rendered as a
// dot on the scrubber + a popover with the comment body when the
// playhead is near the pin.
//
// The actual video element is kept simple (HTML5 <video>); the
// pins layer overlays the bottom of the player. Clicking a pin
// seeks the video to that timestamp.
import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'

import { annotationsApi, type Annotation, type VideoTimestampData } from '@/api/annotations'
import { readErrorMessage } from '@/api/client'
import { useAuthBlob } from '@/lib/useAuthBlob'
import { AnnotationToolbar } from './AnnotationToolbar'

interface Props {
  documentId: string
  versionId: string
  videoUrl: string
  canCreate: boolean
}

export function VideoAnnotationLayer({ documentId, versionId, videoUrl, canCreate }: Props) {
  const videoRef = useRef<HTMLVideoElement>(null)
  const [annotations, setAnnotations] = useState<Annotation[]>([])
  const [visible, setVisible] = useState(true)
  const [duration, setDuration] = useState(0)
  // <video> can't attach X-Tenant-ID — fetch via axios into a blob URL.
  const blobUrl = useAuthBlob(videoUrl)

  // Load on mount + on doc/version change. useEffect (not useMemo)
  // because this is a side effect, not a memoized value.
  useEffect(() => {
    let cancelled = false
    annotationsApi.list(documentId, versionId).then((rows) => {
      if (!cancelled) setAnnotations(rows.filter((a) => a.type === 'video_timestamp'))
    }).catch(() => { /* non-fatal */ })
    return () => { cancelled = true }
  }, [documentId, versionId])

  // Depend on blobUrl: the <video> is only mounted once the authed blob
  // resolves (`{blobUrl && <video …>}`), which happens AFTER first render.
  // With empty deps this effect ran while videoRef.current was still null,
  // bailed, and never re-ran — so loadedmetadata was never observed and
  // duration stayed 0, hiding the pin track forever. Re-running on blobUrl
  // attaches the listener to the real element; the immediate apply() covers
  // the case where a cached blob already fired loadedmetadata.
  useEffect(() => {
    const v = videoRef.current
    if (!v) return
    const apply = () => { if (Number.isFinite(v.duration)) setDuration(v.duration) }
    v.addEventListener('loadedmetadata', apply)
    apply()
    return () => v.removeEventListener('loadedmetadata', apply)
  }, [blobUrl])

  const dropPin = async () => {
    const v = videoRef.current
    if (!v) return
    const at = Math.floor(v.currentTime)
    const body = window.prompt(`Comment at ${formatTime(at)}`)
    if (!body) return
    try {
      const data: VideoTimestampData = { at_seconds: at, body }
      const created = await annotationsApi.create(documentId, versionId, {
        page: 1, type: 'video_timestamp', data: data as unknown as Record<string, unknown>,
      })
      setAnnotations((a) => [...a, created])
    } catch (e: unknown) {
      toast.error(readErrorMessage(e) ?? 'Failed to save pin')
    }
  }

  const seek = (sec: number) => {
    const v = videoRef.current
    if (v) v.currentTime = sec
  }

  const pins = annotations
    .map((a) => ({ id: a.id, ...(a.data as unknown as VideoTimestampData) }))
    .sort((p, q) => p.at_seconds - q.at_seconds)

  return (
    <div className="space-y-2">
      <AnnotationToolbar
        kind="video" mode={null}
        onModeChange={(m) => { if (m === 'pin') void dropPin() }}
        visible={visible} onToggleVisible={setVisible} canCreate={canCreate}
      />
      <div className="relative inline-block">
        {blobUrl && <video ref={videoRef} src={blobUrl} controls className="block max-w-full" data-testid="video-element" />}
        {visible && duration > 0 && (
          <div
            className="absolute bottom-12 start-0 end-0 h-1 bg-transparent"
            data-testid="video-pin-track"
          >
            {pins.map((p) => (
              <button
                key={p.id}
                onClick={() => seek(p.at_seconds)}
                title={`${formatTime(p.at_seconds)} — ${p.body}`}
                style={{ left: `${(p.at_seconds / duration) * 100}%` }}
                className="absolute -top-1 h-3 w-3 -translate-x-1/2 rounded-full border-2 border-background bg-primary shadow-neu-sm"
                data-testid={`video-pin-${p.id}`}
              />
            ))}
          </div>
        )}
      </div>
      {visible && pins.length > 0 && (
        <ul className="text-xs text-[var(--color-text-secondary)] space-y-0.5">
          {pins.map((p) => (
            <li key={p.id}>
              <button onClick={() => seek(p.at_seconds)} className="font-mono text-blue-700 hover:underline dark:text-blue-300">
                {formatTime(p.at_seconds)}
              </button>
              {' '}— {p.body}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function formatTime(sec: number): string {
  const m = Math.floor(sec / 60).toString().padStart(2, '0')
  const s = Math.floor(sec % 60).toString().padStart(2, '0')
  return `${m}:${s}`
}
