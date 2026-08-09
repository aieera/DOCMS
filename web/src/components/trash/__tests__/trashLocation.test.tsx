// Trash rows must say where a restore will land (BUG-13).
//
// The trash tables listed Title / Size / Deleted and nothing else, so
// "Restore" was a blind action — there was no way to tell which
// workspace or folder the document would reappear in.
//
// The other half of the requirement is a NEGATIVE one: neither trash
// endpoint returns a purge or retention date
// (services/document/internal/handler/trash_handler.go trashEntry has
// id/title/workspace_id/folder_id/mime_type/total_size_bytes/
// lifecycle_state/created_by*/deleted_by*/deleted_at/user_cleared and
// nothing else), so no such column exists and none must be invented.

import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'

import { TrashLocation } from '@/components/trash/TrashLocation'

describe('<TrashLocation>', () => {
  it('shows workspace and folder', () => {
    render(<TrashLocation location={{ workspaceName: 'Legal', folderName: 'Contracts' }} />)
    expect(screen.getByText(/Legal/)).toBeInTheDocument()
    expect(screen.getByText(/Contracts/)).toBeInTheDocument()
  })

  it('degrades to the workspace when the folder cannot be resolved', () => {
    // A folder deleted in the same cascade as the document 404s on
    // lookup. Showing the workspace alone is honest; guessing is not.
    render(<TrashLocation location={{ workspaceName: 'Legal' }} />)
    expect(screen.getByText(/Legal/)).toBeInTheDocument()
  })

  it('renders an em dash rather than inventing a location', () => {
    render(<TrashLocation location={{}} />)
    expect(screen.getByText('—')).toBeInTheDocument()
  })
})
