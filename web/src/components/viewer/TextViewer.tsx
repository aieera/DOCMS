import { useEffect, useState } from 'react'

export function TextViewer({ url }: { url: string; mimeType?: string }) {
  const [content, setContent] = useState('')
  useEffect(() => { fetch(url).then((r) => r.text()).then(setContent).catch(() => setContent('Failed to load')) }, [url])
  return <pre className="max-h-[80vh] overflow-auto rounded-lg bg-slate-50 p-4 font-mono text-sm leading-relaxed dark:bg-slate-800">{content}</pre>
}
