import { Check, Monitor, Moon, Sun } from 'lucide-react'
import { useTheme, type ThemeMode } from './theme-provider'
import { Button } from '@/components/ui/shadcn/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/shadcn/dropdown-menu'

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
        <Button variant="ghost" size="icon" aria-label="Toggle theme">
          <ActiveIcon className="h-[1.1rem] w-[1.1rem]" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-[160px]">
        {items.map(({ value, label, icon: Icon }) => (
          <DropdownMenuItem key={value} onSelect={() => setMode(value)}>
            <Icon className="h-4 w-4" />
            <span className="flex-1">{label}</span>
            {mode === value && <Check className="h-3.5 w-3.5 text-muted-foreground" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
