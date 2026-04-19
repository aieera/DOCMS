export function VideoPlayer({ url }: { url: string; mimeType?: string }) {
  return <video src={url} controls className="mx-auto max-h-[80vh] max-w-full rounded-lg" />
}
