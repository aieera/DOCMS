import { useAuthBlob } from '@/lib/useAuthBlob'

export function VideoPlayer({ url }: { url: string; mimeType?: string }) {
  // <video> can't attach X-Tenant-ID — fetch via axios into a blob URL.
  const blobUrl = useAuthBlob(url)
  if (!blobUrl) return null
  return <video src={blobUrl} controls className="mx-auto max-h-[80vh] max-w-full rounded-lg" />
}
