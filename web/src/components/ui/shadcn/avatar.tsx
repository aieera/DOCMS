import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from 'react'
import * as AvatarPrimitive from '@radix-ui/react-avatar'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/cn'

// Canonical shadcn Avatar surface (Avatar / AvatarImage / AvatarFallback)
// + a sugar `name` + `size` shorthand to mirror the bespoke ./Avatar
// API so the strangler import-swap stays mechanical.

const avatarVariants = cva(
  'relative flex shrink-0 overflow-hidden rounded-full bg-muted',
  {
    variants: {
      size: {
        sm: 'h-6 w-6 text-[10px]',
        md: 'h-8 w-8 text-xs',
        lg: 'h-10 w-10 text-sm',
      },
    },
    defaultVariants: { size: 'md' },
  },
)

const AvatarRoot = forwardRef<
  ElementRef<typeof AvatarPrimitive.Root>,
  ComponentPropsWithoutRef<typeof AvatarPrimitive.Root> & VariantProps<typeof avatarVariants>
>(({ className, size, ...props }, ref) => (
  <AvatarPrimitive.Root ref={ref} className={cn(avatarVariants({ size }), className)} {...props} />
))
AvatarRoot.displayName = AvatarPrimitive.Root.displayName

const AvatarImage = forwardRef<
  ElementRef<typeof AvatarPrimitive.Image>,
  ComponentPropsWithoutRef<typeof AvatarPrimitive.Image>
>(({ className, ...props }, ref) => (
  <AvatarPrimitive.Image ref={ref} className={cn('aspect-square h-full w-full object-cover', className)} {...props} />
))
AvatarImage.displayName = AvatarPrimitive.Image.displayName

const AvatarFallback = forwardRef<
  ElementRef<typeof AvatarPrimitive.Fallback>,
  ComponentPropsWithoutRef<typeof AvatarPrimitive.Fallback>
>(({ className, ...props }, ref) => (
  <AvatarPrimitive.Fallback
    ref={ref}
    className={cn('flex h-full w-full items-center justify-center rounded-full bg-primary text-primary-foreground font-medium', className)}
    {...props}
  />
))
AvatarFallback.displayName = AvatarPrimitive.Fallback.displayName

// Convenience wrapper preserving the bespoke API. Use the raw
// AvatarRoot + AvatarImage + AvatarFallback for richer cases
// (multiple fallbacks, status dots, etc).
interface ShortAvatarProps extends VariantProps<typeof avatarVariants> {
  src?: string
  name: string
  className?: string
}

function Avatar({ src, name, size, className }: ShortAvatarProps) {
  const initials = name.split(' ').map((w) => w[0]).filter(Boolean).join('').slice(0, 2).toUpperCase()
  return (
    <AvatarRoot size={size} className={className}>
      {src && <AvatarImage src={src} alt={name} />}
      <AvatarFallback>{initials}</AvatarFallback>
    </AvatarRoot>
  )
}

export { Avatar, AvatarRoot, AvatarImage, AvatarFallback }
