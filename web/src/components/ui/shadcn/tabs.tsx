import { forwardRef, type ComponentPropsWithoutRef, type ElementRef } from 'react'
import * as TabsPrimitive from '@radix-ui/react-tabs'
import { cn } from '@/lib/cn'
import { useDirection } from '@/hooks/useDirection'

// Radix's Tabs.Root defaults to dir="ltr" when the prop is omitted -- it
// does NOT inherit from the document -- and it stamps that onto a wrapper
// div around the whole tab set. So every tab panel in the app (18 files
// import this) laid itself out LTR inside the Arabic UI: headings hugged
// the wrong edge and panel grids never mirrored.
//
// This also made the RTL geometry check useless on tabbed pages. It reports
// what RTL breaks that LTR holds, and the answer was "nothing" -- not
// because the layout was sound, but because RTL was rendering byte-identical
// to LTR. A clean differential is not the same as a correct page.
//
// useDirection() is the same source app-sidebar and DirectionalIcon read.
// An explicit dir prop still wins, for the rare genuinely-physical tab set.
const Tabs = forwardRef<
  ElementRef<typeof TabsPrimitive.Root>,
  ComponentPropsWithoutRef<typeof TabsPrimitive.Root>
>(({ dir, ...props }, ref) => {
  const direction = useDirection()
  return <TabsPrimitive.Root ref={ref} dir={dir ?? direction} {...props} />
})
Tabs.displayName = TabsPrimitive.Root.displayName

const TabsList = forwardRef<
  ElementRef<typeof TabsPrimitive.List>,
  ComponentPropsWithoutRef<typeof TabsPrimitive.List>
>(({ className, ...props }, ref) => (
  <TabsPrimitive.List
    ref={ref}
    className={cn(
      'inline-flex h-9 items-center justify-start rounded-xl bg-muted p-1 text-muted-foreground shadow-neu-inset',
      // Triggers are whitespace-nowrap, so a four- or five-tab set simply
      // ran off the side of a phone and those tabs could not be reached
      // at all. Cap the list at its container and let it scroll instead;
      // the scrollbar itself is hidden so the pill still reads as a pill.
      'max-w-full overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden',
      className,
    )}
    {...props}
  />
))
TabsList.displayName = TabsPrimitive.List.displayName

const TabsTrigger = forwardRef<
  ElementRef<typeof TabsPrimitive.Trigger>,
  ComponentPropsWithoutRef<typeof TabsPrimitive.Trigger>
>(({ className, ...props }, ref) => (
  <TabsPrimitive.Trigger
    ref={ref}
    className={cn(
      'inline-flex items-center justify-center whitespace-nowrap rounded-md px-3 py-1 text-sm font-medium text-muted-foreground ring-offset-background transition-all',
      'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
      'disabled:pointer-events-none disabled:opacity-50',
      'data-[state=active]:bg-background data-[state=active]:text-primary data-[state=active]:shadow-neu-sm',
      className,
    )}
    {...props}
  />
))
TabsTrigger.displayName = TabsPrimitive.Trigger.displayName

const TabsContent = forwardRef<
  ElementRef<typeof TabsPrimitive.Content>,
  ComponentPropsWithoutRef<typeof TabsPrimitive.Content>
>(({ className, ...props }, ref) => (
  <TabsPrimitive.Content
    ref={ref}
    className={cn(
      'mt-2 ring-offset-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
      className,
    )}
    {...props}
  />
))
TabsContent.displayName = TabsPrimitive.Content.displayName

export { Tabs, TabsList, TabsTrigger, TabsContent }
