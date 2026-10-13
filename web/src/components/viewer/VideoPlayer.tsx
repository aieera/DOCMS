import { useAuthBlob } from '@/lib/useAuthBlob'

export function VideoPlayer({ url }: { url: string; mimeType?: string }) {
  // Fetched via the axios client into a blob URL (see useAuthBlob).
  const blobUrl = useAuthBlob(url)
  if (!blobUrl) return null
  return <video src={blobUrl} controls className="mx-auto max-h-[80vh] max-w-full rounded-lg" />
}
