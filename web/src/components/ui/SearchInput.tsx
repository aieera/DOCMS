import { useEffect, useRef, useState } from 'react'
import { Search, X } from 'lucide-react'
import { cn } from '@/lib/cn'

interface SearchInputProps {
  value: string
  onChange: (v: string) => void
  placeholder?: string
  debounceMs?: number
  className?: string
  autoFocus?: boolean
}

export function SearchInput({ value, onChange, placeholder = 'Search...', debounceMs = 300, className, autoFocus }: SearchInputProps) {
  const [local, setLocal] = useState(value)
  const timer = useRef<ReturnType<typeof setTimeout>>()

  useEffect(() => { setLocal(value) }, [value])

  const handleChange = (v: string) => {
    setLocal(v)
    clearTimeout(timer.current)
    timer.current = setTimeout(() => onChange(v), debounceMs)
  }

  return (
    <div className={cn('relative', className)}>
      <Search className="pointer-events-none absolute inset-y-0 start-3 my-auto h-4 w-4 text-[var(--color-text-secondary)]" />
      <input
        value={local}
        onChange={(e) => handleChange(e.target.value)}
        placeholder={placeholder}
        autoFocus={autoFocus}
        className="h-10 w-full rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] ps-9 pe-9 text-sm outline-none transition-colors focus:ring-2 focus:ring-[var(--color-primary)]"
      />
      {local && (
        <button onClick={() => handleChange('')} className="absolute inset-y-0 end-3 my-auto" aria-label="Clear">
          <X className="h-4 w-4 text-[var(--color-text-secondary)]" />
        </button>
      )}
    </div>
  )
}
