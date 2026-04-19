import * as AvatarPrimitive from '@radix-ui/react-avatar'
import { cn } from '@/lib/cn'

interface AvatarProps { src?: string; name: string; size?: 'sm' | 'md' | 'lg'; className?: string }

const sizeMap = { sm: 'h-6 w-6 text-[10px]', md: 'h-8 w-8 text-xs', lg: 'h-10 w-10 text-sm' }

export function Avatar({ src, name, size = 'md', className }: AvatarProps) {
  const initials = name.split(' ').map((w) => w[0]).join('').slice(0, 2).toUpperCase()
  return (
    <AvatarPrimitive.Root className={cn('relative inline-flex shrink-0 overflow-hidden rounded-full', sizeMap[size], className)}>
      <AvatarPrimitive.Image src={src} alt={name} className="h-full w-full object-cover" />
      <AvatarPrimitive.Fallback className="flex h-full w-full items-center justify-center bg-[var(--color-primary)] font-medium text-white">
        {initials}
      </AvatarPrimitive.Fallback>
    </AvatarPrimitive.Root>
  )
}
