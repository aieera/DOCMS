import {
  cloneElement,
  createContext,
  forwardRef,
  isValidElement,
  useContext,
  useId,
  type HTMLAttributes,
  type ReactElement,
} from 'react'
import {
  Controller,
  FormProvider,
  useFormContext,
  type ControllerProps,
  type FieldPath,
  type FieldValues,
} from 'react-hook-form'
import { cn } from '@/lib/cn'
import { Label } from './label'

// Canonical shadcn-style Form helpers wrapping react-hook-form. Use
// `<Form {...form}>` (where `form = useForm({...})`), then compose
// fields as `<FormField control={form.control} name="x"
// render={({ field }) => (<FormItem>...) />`. FormItem provides ids
// + aria wiring so screen readers pair the Label, control,
// description, and error message correctly.

const Form = FormProvider

interface FormFieldContextValue<
  TFieldValues extends FieldValues = FieldValues,
  TName extends FieldPath<TFieldValues> = FieldPath<TFieldValues>,
> {
  name: TName
}

const FormFieldContext = createContext<FormFieldContextValue | null>(null)

function FormField<
  TFieldValues extends FieldValues = FieldValues,
  TName extends FieldPath<TFieldValues> = FieldPath<TFieldValues>,
>(props: ControllerProps<TFieldValues, TName>) {
  return (
    <FormFieldContext.Provider value={{ name: props.name } as FormFieldContextValue}>
      <Controller {...props} />
    </FormFieldContext.Provider>
  )
}

interface FormItemContextValue {
  id: string
}
const FormItemContext = createContext<FormItemContextValue | null>(null)

function useFormField() {
  const fieldContext = useContext(FormFieldContext)
  const itemContext = useContext(FormItemContext)
  const { getFieldState, formState } = useFormContext()
  if (!fieldContext) throw new Error('useFormField must be used inside <FormField>')
  if (!itemContext) throw new Error('useFormField must be used inside <FormItem>')
  const fieldState = getFieldState(fieldContext.name, formState)
  const { id } = itemContext
  return {
    id,
    name: fieldContext.name,
    formItemId: `${id}-form-item`,
    formDescriptionId: `${id}-form-item-description`,
    formMessageId: `${id}-form-item-message`,
    ...fieldState,
  }
}

const FormItem = forwardRef<HTMLDivElement, HTMLAttributes<HTMLDivElement>>(
  ({ className, ...props }, ref) => {
    const id = useId()
    return (
      <FormItemContext.Provider value={{ id }}>
        <div ref={ref} className={cn('space-y-1.5', className)} {...props} />
      </FormItemContext.Provider>
    )
  },
)
FormItem.displayName = 'FormItem'

const FormLabel = forwardRef<HTMLLabelElement, HTMLAttributes<HTMLLabelElement>>(
  ({ className, ...props }, ref) => {
    const { error, formItemId } = useFormField()
    return (
      <Label ref={ref} className={cn(error && 'text-destructive', className)} htmlFor={formItemId} {...props} />
    )
  },
)
FormLabel.displayName = 'FormLabel'

// FormControl clones its single child to inject the field-aware
// id + aria attributes — saves callers from writing `id`,
// `aria-invalid`, and `aria-describedby` on every input.
function FormControl({ children }: { children: ReactElement }) {
  const { error, formItemId, formDescriptionId, formMessageId } = useFormField()
  if (!isValidElement(children)) return children
  return cloneElement(children as ReactElement<Record<string, unknown>>, {
    id: formItemId,
    'aria-describedby': !error ? formDescriptionId : `${formDescriptionId} ${formMessageId}`,
    'aria-invalid': !!error,
  })
}

const FormDescription = forwardRef<HTMLParagraphElement, HTMLAttributes<HTMLParagraphElement>>(
  ({ className, ...props }, ref) => {
    const { formDescriptionId } = useFormField()
    return (
      <p ref={ref} id={formDescriptionId} className={cn('text-xs text-muted-foreground', className)} {...props} />
    )
  },
)
FormDescription.displayName = 'FormDescription'

const FormMessage = forwardRef<HTMLParagraphElement, HTMLAttributes<HTMLParagraphElement>>(
  ({ className, children, ...props }, ref) => {
    const { error, formMessageId } = useFormField()
    const body = error ? String(error?.message ?? '') : children
    if (!body) return null
    return (
      <p ref={ref} id={formMessageId} className={cn('text-xs font-medium text-destructive', className)} {...props}>
        {body}
      </p>
    )
  },
)
FormMessage.displayName = 'FormMessage'

export {
  useFormField,
  Form,
  FormField,
  FormItem,
  FormLabel,
  FormControl,
  FormDescription,
  FormMessage,
}
