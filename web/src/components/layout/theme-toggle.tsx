import { Monitor, Moon, Sun, Check } from 'lucide-react'
import { useTheme, type ThemeMode } from './theme-provider'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/DropdownMenu'

const items: { value: ThemeMode; label: string; icon: typeof Sun }[] = [
  { value: 'light', label: 'Light', icon: Sun },
  { value: 'dark', label: 'Dark', icon: Moon },
  { value: 'system', label: 'System', icon: Monitor },
]

export function ThemeToggle() {
  const { mode, resolved, setMode } = useTheme()
  const ActiveIcon = resolved === 'dark' ? Moon : Sun
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label="Toggle theme"
          className="inline-flex h-9 w-9 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <ActiveIcon className="h-[1.1rem] w-[1.1rem]" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-[160px]">
        {items.map(({ value, label, icon: Icon }) => (
          <DropdownMenuItem key={value} icon={<Icon className="h-4 w-4" />} onSelect={() => setMode(value)}>
            <span className="flex w-full items-center justify-between">
              <span>{label}</span>
              {mode === value && <Check className="h-3.5 w-3.5 text-muted-foreground" />}
            </span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
