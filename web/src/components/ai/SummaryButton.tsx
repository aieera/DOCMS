import { useState } from 'react'
import * as Popover from '@radix-ui/react-popover'
import { Button } from '@/components/ui/Button'
import { Spinner } from '@/components/ui/Spinner'
import { Sparkles } from 'lucide-react'
import { summarizeDocument } from '@/api/intelligence'

export function SummaryButton({ documentId, text }: { documentId: string; text: string }) {
  const [summary, setSummary] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const generate = async () => {
    if (summary || loading) return
    setLoading(true)
    try {
      const resp = await summarizeDocument(documentId, text, 'short')
      setSummary(resp.summary)
    } catch {
      setSummary('Failed to generate summary.')
    } finally {
      setLoading(false)
    }
  }

  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <Button variant="ghost" size="sm" onClick={generate}><Sparkles className="h-4 w-4" /> Summarize</Button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content sideOffset={4} className="z-50 w-80 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4 shadow-lg">
          {loading ? <Spinner /> : <p className="text-sm">{summary || 'Click to generate a summary'}</p>}
          <Popover.Arrow className="fill-[var(--color-bg-secondary)]" />
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  )
}
