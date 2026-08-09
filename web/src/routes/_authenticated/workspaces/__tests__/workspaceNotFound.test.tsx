// Non-existent workspace (BUG-07).
//
// /workspaces/00000000-0000-0000-0000-000000000000 used to render a
// complete, operable file browser — breadcrumb, "All files", live New
// folder / New note / Upload buttons — for a workspace that does not
// exist. The only signal anything was wrong was a toast carrying the
// raw backend status string, and every button the user then pressed
// produced another one.
//
// Two halves are pinned here: the classifier that decides a failure is
// terminal, and the surface that replaces the page when it is (which
// must carry no create/upload affordance at all).

import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import type { ReactNode } from 'react'
import { vi } from 'vitest'

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  // href, not a bare <a>: without it the element has no `link` role and
  // the "is there still a way out of here?" assertion can't see it.
  Link: ({ children, to, params: _p, ...rest }: { children?: ReactNode } & Record<string, unknown>) => (
    <a href={String(to ?? '#')} {...rest}>{children}</a>
  ),
}))

// The route module transitively imports the document viewer, which
// pulls pdf.js — it touches DOMMatrix at import time and jsdom has no
// such global. Nothing under test renders it.
vi.mock('@/components/viewer/DocumentPreview', () => ({ DocumentPreview: () => null }))
vi.mock('@/components/viewer/PDFViewer', () => ({ PDFViewer: () => null }))
vi.mock('@/components/viewer/PDFLayoutViewer', () => ({ PDFLayoutViewer: () => null }))

import {
  WorkspaceUnavailable,
  workspaceFailureKind,
} from '../$workspaceId/index'

const axiosErr = (status: number) => ({ response: { status } })

describe('workspaceFailureKind', () => {
  it('treats a malformed workspace id (400 INVALID_ARGUMENT) as not-found', () => {
    // The reported URL. The backend rejects the id before looking
    // anything up, so it answers 400 — which for a URL the user opened
    // means precisely "no such workspace".
    expect(workspaceFailureKind(axiosErr(400))).toBe('not-found')
  })

  it('treats 404 as not-found and 403 as forbidden', () => {
    expect(workspaceFailureKind(axiosErr(404))).toBe('not-found')
    expect(workspaceFailureKind(axiosErr(403))).toBe('forbidden')
  })

  it('does NOT take over the page for transient failures', () => {
    // A 5xx or a dropped connection must leave the normal page (and its
    // own retry paths) in place rather than claiming the workspace is
    // gone.
    expect(workspaceFailureKind(axiosErr(500))).toBeNull()
    expect(workspaceFailureKind(axiosErr(502))).toBeNull()
    expect(workspaceFailureKind(new Error('Network Error'))).toBeNull()
    expect(workspaceFailureKind(null)).toBeNull()
  })
})

describe('<WorkspaceUnavailable>', () => {
  it('says the workspace is not found and offers a way back', () => {
    render(<WorkspaceUnavailable kind="not-found" />)

    expect(screen.getByTestId('workspace-not-found')).toBeInTheDocument()
    expect(screen.getByText(/workspace not found/i)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /back to workspaces/i })).toBeInTheDocument()
  })

  it('offers NO create or upload affordance', () => {
    render(<WorkspaceUnavailable kind="not-found" />)

    // The whole point: there must be nothing left to press that would
    // fire a doomed request against a workspace that isn't there.
    expect(screen.queryByRole('button', { name: /upload/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /new folder/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /new note/i })).not.toBeInTheDocument()
  })

  it('distinguishes "no access" from "does not exist"', () => {
    render(<WorkspaceUnavailable kind="forbidden" />)

    expect(screen.getByTestId('workspace-forbidden')).toBeInTheDocument()
    expect(screen.getByText(/don’t have access|don't have access/i)).toBeInTheDocument()
    // Telling someone a workspace doesn't exist when they simply lack
    // access sends them to recreate it.
    expect(screen.queryByText(/workspace not found/i)).not.toBeInTheDocument()
  })
})
