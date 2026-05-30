import { useTranslation } from 'react-i18next'
import { Plus, ChevronDown } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/shadcn/dropdown-menu'
import { Button } from '@/components/ui/shadcn/button'
import { STEP_PRESETS, type StepType } from './step-presets'

interface Props {
  onPick: (type: StepType) => void
}

// StepPresetMenu is the "Add step" dropdown the template editor
// shows above the chain list. Renders all 5 step types with their
// theme-aligned icons; the icon-tinting cls matches what StepChainList
// uses so dragging a new step in produces a visually consistent chip.
export function StepPresetMenu({ onPick }: Props) {
  const { t } = useTranslation('workflows')
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm" data-testid="step-preset-menu">
          <Plus className="me-1 h-3.5 w-3.5" />
          {t('editor.add_step')}
          <ChevronDown className="ms-1 h-3.5 w-3.5" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-56">
        {STEP_PRESETS.map((p) => {
          const Icon = p.icon
          return (
            <DropdownMenuItem
              key={p.type}
              onClick={() => onPick(p.type)}
              className="flex items-center gap-2"
              data-testid={`step-preset-${p.type}`}
            >
              <span
                className={`flex h-6 w-6 items-center justify-center rounded-md border ${p.toneCls}`}
                aria-hidden
              >
                <Icon className="h-3.5 w-3.5" />
              </span>
              <span className="text-sm">{t(`editor.step.${p.type}`)}</span>
            </DropdownMenuItem>
          )
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
