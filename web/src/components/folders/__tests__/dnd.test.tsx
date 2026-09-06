import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createPortal } from 'react-dom'
import { DndContext } from '@dnd-kit/core'

import { DraggableTile } from '../dnd'

// Dialogs and menus opened from a tile render through React portals, so
// their keyboard events bubble through the REACT tree — into the tile's
// dnd-kit listeners — even though their DOM nodes live under
// document.body. dnd-kit's keyboard activator treats Space/Enter as
// "start drag" and calls preventDefault, which silently ate those keys
// in any dialog opened from a tile (QA SD-03: renaming a document could
// not contain spaces).
function PortalInput() {
  return createPortal(
    <input aria-label="portal-input" defaultValue="" />,
    document.body,
  )
}

describe('DraggableTile', () => {
  it('does not swallow keystrokes typed in portal-rendered children', async () => {
    render(
      <DndContext>
        <DraggableTile dndId="doc:1" data={{ kind: 'doc', id: '1', label: 'Doc' }}>
          <PortalInput />
        </DraggableTile>
      </DndContext>,
    )
    const input = screen.getByLabelText('portal-input')
    await userEvent.type(input, 'Test One Two')
    expect(input).toHaveValue('Test One Two')
  })
})
