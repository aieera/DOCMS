import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
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
  const { t } = useTranslation('common')
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
      ? t('adm.meta.err_enum')
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
          <DialogTitle>{field ? t('adm.meta.dlg_edit', { name: field.name }) : t('adm.meta.dlg_add')}</DialogTitle>
          <DialogDescription>{t('adm.meta.dlg_desc')}</DialogDescription>
        </DialogHeader>

        <div className="space-y-4">
          <Input
            label={t('adm.meta.f_name')}
            dir="ltr"
            value={draft.name}
            onChange={(e) => set('name', e.target.value)}
            onBlur={() => setTouched(true)}
            placeholder="invoice_number"
            error={touched && nameError ? nameError : undefined}
            autoFocus
            data-testid="field-name"
          />

          <Input
            label={t('adm.meta.f_label')}
            value={draft.label ?? ''}
            onChange={(e) => set('label', e.target.value)}
            placeholder={t('adm.meta.f_label_ph')}
            data-testid="field-label"
          />

          <Select
            label={t('adm.meta.f_type')}
            value={draft.type}
            onValueChange={(v) => set('type', v as FieldType)}
            options={FIELD_TYPES.map((ft) => ({
              value: ft.value,
              label: t('adm.meta.type_option', {
                label: t(`adm.meta.t_${ft.value}`, { defaultValue: ft.label }),
                hint: t(`adm.meta.t_${ft.value}_hint`, { defaultValue: ft.hint }),
              }),
            }))}
          />

          <Input
            label={t('adm.meta.f_desc')}
            value={draft.description ?? ''}
            onChange={(e) => set('description', e.target.value)}
            placeholder={t('adm.meta.f_desc_ph')}
            data-testid="field-description"
          />

          {draft.type === 'enum' && (
            <div className="space-y-1.5">
              <span className="text-sm font-medium">{t('adm.meta.choices')}</span>
              {choices.map((c, i) => (
                <div key={i} className="flex items-center gap-2">
                  <Input
                    value={c}
                    onChange={(e) => setChoices((cs) => cs.map((x, j) => (j === i ? e.target.value : x)))}
                    placeholder={t('adm.meta.choice_n', { n: i + 1 })}
                    data-testid={`field-choice-${i}`}
                  />
                  <Button
                    type="button" variant="ghost" size="icon"
                    onClick={() => setChoices((cs) => (cs.length === 1 ? [''] : cs.filter((_, j) => j !== i)))}
                    aria-label={t('adm.meta.remove_choice', { n: i + 1 })}
                  >
                    <X className="h-4 w-4" />
                  </Button>
                </div>
              ))}
              <Button type="button" variant="outline" size="sm" onClick={() => setChoices((cs) => [...cs, ''])} data-testid="field-add-choice">
                <Plus className="me-1 h-4 w-4" /> {t('adm.meta.add_choice')}
              </Button>
              {touched && enumError && <p className="text-xs font-medium text-destructive">{enumError}</p>}
            </div>
          )}

          {draft.type === 'array' && (
            <Select
              label={t('adm.meta.item_type')}
              value={draft.itemType ?? 'string'}
              onValueChange={(v) => set('itemType', v as SchemaField['itemType'])}
              options={[
                { value: 'string', label: t('adm.meta.t_string') },
                { value: 'number', label: t('adm.meta.t_number') },
                { value: 'integer', label: t('adm.meta.t_integer') },
                { value: 'boolean', label: t('adm.meta.t_boolean') },
              ]}
            />
          )}

          {draft.type === 'string' && (
            <div className="grid gap-3 sm:grid-cols-3">
              <Input label={t('adm.meta.min_length')} type="number" min={0}
                value={draft.minLength ?? ''}
                onChange={(e) => set('minLength', e.target.value === '' ? undefined : Number(e.target.value))} />
              <Input label={t('adm.meta.max_length')} type="number" min={0}
                value={draft.maxLength ?? ''}
                onChange={(e) => set('maxLength', e.target.value === '' ? undefined : Number(e.target.value))} />
              <Input label={t('adm.meta.pattern')} dir="ltr"
                value={draft.pattern ?? ''}
                onChange={(e) => set('pattern', e.target.value)}
                placeholder="^INV-" />
            </div>
          )}

          {numeric && (
            <div className="grid gap-3 sm:grid-cols-2">
              <Input label={t('adm.meta.minimum')} type="number"
                value={draft.minimum ?? ''}
                onChange={(e) => set('minimum', e.target.value === '' ? undefined : Number(e.target.value))} />
              <Input label={t('adm.meta.maximum')} type="number"
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
            {t('adm.meta.required_note')}
          </label>
        </div>

        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>{t('adm.meta.cancel')}</Button>
          <Button onClick={submit} data-testid="field-save">{field ? t('adm.meta.save_field') : t('adm.meta.dlg_add')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
