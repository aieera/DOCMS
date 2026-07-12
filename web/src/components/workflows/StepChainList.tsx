import {
  DndContext,
  PointerSensor,
  KeyboardSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core'
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { useTranslation } from 'react-i18next'
import { AlertTriangle, GripVertical } from 'lucide-react'
import { cn } from '@/lib/cn'
import type { ADR0073Step } from '@/api/workflows'
import { presetFor } from './step-presets'

interface Props {
  steps: ADR0073Step[]
  selectedId: string | null
  onSelect: (id: string) => void
  onReorder: (next: ADR0073Step[]) => void
  // issueIds is the set of step ids that have validation issues —
  // used to flag the chip with a small warning icon.
  issueIds: Set<string>
}

// StepChainList is the centre pane of the template editor: a vertical
// chain of step chips with connector lines, drag-to-reorder via
// dnd-kit, click-to-select. Validation issues on a step render as a
// small warning glyph on the chip.
//
// Drag handle is the GripVertical icon — clicking anywhere else
// selects the step. Keyboard reorder (Tab to focus, Space to pick
// up, arrows to move, Space to drop) is built into @dnd-kit's
// SortableContext + KeyboardSensor.
export function StepChainList({ steps, selectedId, onSelect, onReorder, issueIds }: Props) {
  const { t } = useTranslation('workflows')
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )

  if (steps.length === 0) {
    return (
      <p className="rounded-md border border-dashed border-border bg-muted/30 p-6 text-center text-sm text-muted-foreground">
        {t('editor.chain_empty')}
      </p>
    )
  }

  const handleDragEnd = (e: DragEndEvent) => {
    const { active, over } = e
    if (!over || active.id === over.id) return
    const from = steps.findIndex((s) => s.id === active.id)
    const to = steps.findIndex((s) => s.id === over.id)
    if (from < 0 || to < 0) return
    onReorder(arrayMove(steps, from, to))
  }

  return (
    <DndContext sensors={sensors} collisionDetection={closestCenter} onDragEnd={handleDragEnd}>
      <SortableContext items={steps.map((s) => s.id)} strategy={verticalListSortingStrategy}>
        <ol className="space-y-2" data-testid="step-chain-list">
          {steps.map((step, idx) => (
            <SortableChip
              key={step.id}
              step={step}
              isSelected={step.id === selectedId}
              hasIssue={issueIds.has(step.id)}
              isLast={idx === steps.length - 1}
              onSelect={() => onSelect(step.id)}
            />
          ))}
        </ol>
      </SortableContext>
    </DndContext>
  )
}

interface ChipProps {
  step: ADR0073Step
  isSelected: boolean
  hasIssue: boolean
  isLast: boolean
  onSelect: () => void
}

function SortableChip({ step, isSelected, hasIssue, isLast, onSelect }: ChipProps) {
  const { t } = useTranslation('workflows')
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } = useSortable({
    id: step.id,
  })
  const preset = presetFor(step.type)
  const Icon = preset.icon
  return (
    <li
      ref={setNodeRef}
      style={{
        transform: CSS.Transform.toString(transform),
        transition,
        opacity: isDragging ? 0.5 : 1,
      }}
      className="relative ps-10"
    >
      {!isLast && (
        <span
          aria-hidden
          className="absolute start-4 top-9 -ms-px h-[calc(100%-1rem)] w-0.5 bg-border"
        />
      )}
      <span
        className={cn(
          'absolute start-0 top-1 flex h-8 w-8 items-center justify-center rounded-full border',
          preset.toneCls,
        )}
        aria-hidden
      >
        <Icon className="h-4 w-4" />
      </span>
      <div
        className={cn(
          'flex items-center gap-2 rounded-xl border p-3 transition-colors',
          isSelected
            ? 'border-primary bg-primary/10'
            : 'border-border bg-card hover:border-primary/40 hover:bg-muted/40',
        )}
      >
        <button
          type="button"
          ref={setNodeRef}
          {...attributes}
          {...listeners}
          className="text-muted-foreground hover:text-foreground"
          aria-label="Drag to reorder"
          data-testid={`step-drag-${step.id}`}
        >
          <GripVertical className="h-4 w-4" />
        </button>
        <button
          type="button"
          onClick={onSelect}
          className="min-w-0 flex-1 text-start"
          data-testid={`step-chip-${step.id}`}
        >
          <p className="truncate text-sm font-medium">
            {step.name || t(`editor.step.${step.type}`)}
          </p>
          <p className="mt-0.5 text-[10px] uppercase tracking-wide text-muted-foreground">
            {t(`editor.step.${step.type}`)}
            <span className="ms-2 font-mono normal-case">{step.id}</span>
          </p>
        </button>
        {hasIssue && (
          <AlertTriangle
            className="h-4 w-4 text-warning"
            aria-label="Validation issue"
            data-testid={`step-issue-${step.id}`}
          />
        )}
      </div>
    </li>
  )
}
