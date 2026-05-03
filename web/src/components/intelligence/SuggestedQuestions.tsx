import { Sparkles } from 'lucide-react'

interface Props {
  onPick: (question: string) => void
}

// Static for v1. A follow-up makes a small LLM call when the panel
// opens to derive document-specific suggestions; until then these
// generic prompts cover the 80% case.
const DEFAULTS = [
  'What are the key findings?',
  'Summarize this document in one paragraph.',
  'What dates and amounts are mentioned?',
  'What action items are listed?',
]

export function SuggestedQuestions({ onPick }: Props) {
  return (
    <div className="flex flex-wrap gap-2 px-4 py-2 text-xs">
      <span className="flex items-center gap-1 text-zinc-500">
        <Sparkles className="h-3 w-3" />
        Suggested:
      </span>
      {DEFAULTS.map((q) => (
        <button
          key={q}
          onClick={() => onPick(q)}
          className="rounded-full bg-zinc-100 px-3 py-1 hover:bg-violet-100 dark:bg-zinc-800 dark:hover:bg-violet-950"
        >
          {q}
        </button>
      ))}
    </div>
  )
}
