import { useState } from 'react'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/shadcn/sheet'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { Spinner } from '@/components/ui/Spinner'
import { Send, Bot, User } from 'lucide-react'
import { askQuestion } from '@/api/intelligence'
import type { AIAnswer } from '@/types/api'

interface Message { role: 'user' | 'assistant'; content: string; sources?: AIAnswer['sources'] }
interface Props { open: boolean; onClose: () => void; scope?: string; scopeId?: string }

export function AIChatPanel({ open, onClose, scope, scopeId }: Props) {
  const [messages, setMessages] = useState<Message[]>([])
  const [input, setInput] = useState('')
  const [loading, setLoading] = useState(false)

  const send = async () => {
    if (!input.trim() || loading) return
    const question = input.trim()
    setInput('')
    setMessages((m) => [...m, { role: 'user', content: question }])
    setLoading(true)
    try {
      const resp = await askQuestion(question, scope, scopeId)
      setMessages((m) => [...m, { role: 'assistant', content: resp.answer, sources: resp.sources }])
    } catch {
      setMessages((m) => [...m, { role: 'assistant', content: 'Sorry, something went wrong.' }])
    } finally {
      setLoading(false)
    }
  }

  return (
    <Sheet open={open} onOpenChange={onClose}>
      <SheetContent className="flex flex-col">
        <SheetHeader>
          <SheetTitle>Ask AI</SheetTitle>
        </SheetHeader>
      <div className="flex h-full flex-col">
        <div className="flex-1 space-y-3 overflow-y-auto pb-4">
          {messages.length === 0 && <p className="text-center text-sm text-[var(--color-text-secondary)] py-8">Ask a question about your documents</p>}
          {messages.map((m, i) => (
            <div key={i} className={`flex gap-2 ${m.role === 'user' ? 'justify-end' : ''}`}>
              {m.role === 'assistant' && <Bot className="mt-0.5 h-5 w-5 shrink-0 text-[var(--color-primary)]" />}
              <div className={`max-w-[85%] rounded-lg px-3 py-2 text-sm ${m.role === 'user' ? 'bg-[var(--color-primary)] text-white' : 'bg-slate-100 dark:bg-slate-800'}`}>
                <p className="whitespace-pre-wrap">{m.content}</p>
                {m.sources && m.sources.length > 0 && (
                  <div className="mt-2 border-t border-white/20 pt-1.5 text-xs opacity-80">
                    Sources: {m.sources.map((s, j) => <span key={j} className="underline">{s.document_id.slice(0, 8)}</span>).reduce((a, b) => <>{a}, {b}</>)}
                  </div>
                )}
              </div>
              {m.role === 'user' && <User className="mt-0.5 h-5 w-5 shrink-0" />}
            </div>
          ))}
          {loading && <div className="flex gap-2"><Bot className="h-5 w-5 text-[var(--color-primary)]" /><Spinner /></div>}
        </div>
        <div className="flex gap-2 border-t border-[var(--color-border)] pt-3">
          <Input value={input} onChange={(e) => setInput(e.target.value)} placeholder="Ask a question..." aria-label="Ask a question" onKeyDown={(e) => e.key === 'Enter' && send()} className="flex-1" />
          <Button onClick={send} disabled={loading || !input.trim()} aria-label="Send message"><Send className="h-4 w-4" /></Button>
        </div>
      </div>
      </SheetContent>
    </Sheet>
  )
}
