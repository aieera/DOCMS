import { useEffect, useState } from 'react'
import { Plus, X } from 'lucide-react'

import {
  FIELD_TYPES,
  validateFieldName,
  type FieldType,
  type SchemaField,
} from '@/lib/schemaFields'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogFooter,
} from '@/components/ui/shadcn/dialog'

// Add/edit one metadata field. Knows nothing about JSON Schema — it
// edits a SchemaField and hands it back; lib/schemaFields does the
// translation. Constraints are shown per type so the form only ever
// asks for what that type supports.
interface Props {
  open: boolean
  onOpenChange: (o: boolean) => void
  /** Field being edited; null = adding a new one. */
  field: SchemaField | null
  /** Existing field names, for the duplicate check. */
  takenNames: string[]
  onSubmit: (field: SchemaField, originalName?: string) => void
}

const EMPTY: SchemaField = { name: '', type: 'string', required: false }

export function SchemaFieldDialog({ open, onOpenChange, field, takenNames, onSubmit }: Props) {
  const [draft, setDraft] = useState<SchemaField>(EMPTY)
  const [choices, setChoices] = useState<string[]>([''])
  const [touched, setTouched] = useState(false)

  useEffect(() => {
    if (!open) return
    setDraft(field ?? EMPTY)
    setChoices(field?.enumValues?.length ? [...field.enumValues] : [''])
    setTouched(false)
  }, [open, field])

  const set = <K extends keyof SchemaField>(k: K, v: SchemaField[K]) =>
    setDraft((d) => ({ ...d, [k]: v }))
  const nameError = validateFieldName(draft.name, takenNames, field?.name)
  const enumError =
    draft.type === 'enum' && choices.map((c) => c.trim()).filter(Boolean).length === 0
      ? 'Add at least one choice'
      : null

  const submit = () => {
    setTouched(true)
    if (nameError || enumError) return
    onSubmit(
      {
        ...draft,
        name: draft.name.trim(),
        enumValues: draft.type === 'enum' ? choices : undefined,
      },
      field?.name,
    )
    onOpenChange(false)
  }

  const numeric = draft.type === 'number' || draft.type === 'integer'

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{field ? `Edit “${field.name}”` : 'Add field'}</DialogTitle>
          <DialogDescription>
            Fields appear on every document's custom-metadata form and are validated on save.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <Input
            label="Field name"
            value={draft.name}
            onChange={(e) => set('name', e.target.value)}
            onBlur={() => setTouched(true)}
            placeholder="invoice_number"
            error={touched && nameError ? nameError : undefined}
            autoFocus
            data-testid="field-name"
          />

          <Input
            label="Label (optional)"
            value={draft.label ?? ''}
            onChange={(e) => set('label', e.target.value)}
            placeholder="Amount (USD) — defaults to a tidied field name"
            data-testid="field-label"
          />

          <Select
            label="Type"
            value={draft.type}
            onValueChange={(v) => set('type', v as FieldType)}
            options={FIELD_TYPES.map((t) => ({ value: t.value, label: `${t.label} — ${t.hint}` }))}
          />

          <Input
            label="Description (optional)"
            value={draft.description ?? ''}
            onChange={(e) => set('description', e.target.value)}
            placeholder="Shown as helper text on the document form"
            data-testid="field-description"
          />

          {draft.type === 'enum' && (
            <div className="space-y-1.5">
              <span className="text-sm font-medium">Choices</span>
              {choices.map((c, i) => (
                <div key={i} className="flex items-center gap-2">
                  <Input
                    value={c}
                    onChange={(e) => setChoices((cs) => cs.map((x, j) => (j === i ? e.target.value : x)))}
                    placeholder={`Choice ${i + 1}`}
                    data-testid={`field-choice-${i}`}
                  />
                  <Button
                    type="button" variant="ghost" size="icon"
                    onClick={() => setChoices((cs) => (cs.length === 1 ? [''] : cs.filter((_, j) => j !== i)))}
                    aria-label={`Remove choice ${i + 1}`}
                  >
                    <X className="h-4 w-4" />
                  </Button>
                </div>
              ))}
              <Button type="button" variant="outline" size="sm" onClick={() => setChoices((cs) => [...cs, ''])} data-testid="field-add-choice">
                <Plus className="me-1 h-4 w-4" /> Add choice
              </Button>
              {touched && enumError && <p className="text-xs font-medium text-destructive">{enumError}</p>}
            </div>
          )}

          {draft.type === 'array' && (
            <Select
              label="List item type"
              value={draft.itemType ?? 'string'}
              onValueChange={(v) => set('itemType', v as SchemaField['itemType'])}
              options={[
                { value: 'string', label: 'Text' },
                { value: 'number', label: 'Number' },
                { value: 'integer', label: 'Whole number' },
                { value: 'boolean', label: 'Yes / No' },
              ]}
            />
          )}

          {draft.type === 'string' && (
            <div className="grid gap-3 sm:grid-cols-3">
              <Input label="Min length" type="number" min={0}
                value={draft.minLength ?? ''}
                onChange={(e) => set('minLength', e.target.value === '' ? undefined : Number(e.target.value))} />
              <Input label="Max length" type="number" min={0}
                value={draft.maxLength ?? ''}
                onChange={(e) => set('maxLength', e.target.value === '' ? undefined : Number(e.target.value))} />
              <Input label="Pattern (regex)"
                value={draft.pattern ?? ''}
                onChange={(e) => set('pattern', e.target.value)}
                placeholder="^INV-" />
            </div>
          )}

          {numeric && (
            <div className="grid gap-3 sm:grid-cols-2">
              <Input label="Minimum" type="number"
                value={draft.minimum ?? ''}
                onChange={(e) => set('minimum', e.target.value === '' ? undefined : Number(e.target.value))} />
              <Input label="Maximum" type="number"
                value={draft.maximum ?? ''}
                onChange={(e) => set('maximum', e.target.value === '' ? undefined : Number(e.target.value))} />
            </div>
          )}

          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={draft.required}
              onChange={(e) => set('required', e.target.checked)}
              className="h-4 w-4 rounded border-border accent-primary"
              data-testid="field-required"
            />
            Required — documents can't be saved without a value
          </label>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button onClick={submit} data-testid="field-save">{field ? 'Save field' : 'Add field'}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
