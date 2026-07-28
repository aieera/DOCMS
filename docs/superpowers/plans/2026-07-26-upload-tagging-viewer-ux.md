# Upload, Auto-Tagging & Viewer UX Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Real upload entry point on the dashboard (workspace + folder + AI suggestions), AI tags that stop feeling manual (0.85 auto-apply + visible suggestions), and a document viewer that leads with content (compact toolbar + preview fallback).

**Architecture:** Frontend-only except one default constant mirrored in Python and Go. Every backend API already exists (`/uploads/predict`, tag-suggestion CRUD, watermark status). New UI composes existing hooks (`useUpload`, react-query) and existing components (WarmCard, Dialog, TagSuggestionsPanel).

**Tech Stack:** React 18 + TanStack Query/Router, vitest + Testing Library (+ `vi.mock` for API modules), Go (document service), Python/pytest (intelligence).

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-26-upload-tagging-viewer-ux-design.md`.
- All new interactive elements keep accessible names (axe gate, ADR 0120).
- Web tests: `cd web && npx vitest run <file>`. Python: `cd services/intelligence && python -m pytest tests/test_auto_tag.py -v`.
- UI copy in plain English like surrounding code (no i18n framework in these files).
- Commit after each task; conventional-commit style; co-author line per repo convention.

---

### Task 1: PDF preview falls back to Original when watermark is unavailable

**Files:**
- Modify: `web/src/components/viewer/DocumentPreview.tsx` (PdfPreviewSwitcher, ~line 166-210)
- Test: `web/src/components/viewer/__tests__/PdfPreviewSwitcher.fallback.test.tsx` (create)

**Interfaces:**
- Consumes: `getWatermarkStatus(documentId, versionId)` from `@/api/watermark` (already used by WatermarkedPreview; react-query dedupes the double fetch).
- Produces: no API change; `PdfPreviewSwitcher` keeps the same props.

- [ ] **Step 1: Write the failing test**

```tsx
// web/src/components/viewer/__tests__/PdfPreviewSwitcher.fallback.test.tsx
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { DocumentPreview } from '@/components/viewer/DocumentPreview'

vi.mock('@/api/watermark', () => ({
  getWatermarkStatus: vi.fn().mockResolvedValue({ status: 'none', page_count: 0 }),
}))

function renderWithQuery(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

describe('PdfPreviewSwitcher watermark fallback', () => {
  it('starts in Original mode when no watermarked pages exist', async () => {
    renderWithQuery(
      <DocumentPreview documentId="d1" versionId="v1" mimeType="application/pdf" url="http://x/f.pdf" />,
    )
    // Original button becomes the pressed one once status resolves empty.
    expect(await screen.findByTestId('pdf-mode-original')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.queryByTestId('wm-preview-empty')).not.toBeInTheDocument()
  })
})
```

Adjust the `DocumentPreview` props in the test to the component's actual export signature (check the top of DocumentPreview.tsx; if only an inner switcher is exported for PDFs, render the exported component that routes PDFs to `PdfPreviewSwitcher`).

- [ ] **Step 2: Run test — expect FAIL** (`aria-pressed` stays on watermarked, `wm-preview-empty` present)

Run: `cd web && npx vitest run src/components/viewer/__tests__/PdfPreviewSwitcher.fallback.test.tsx`

- [ ] **Step 3: Implement fallback in PdfPreviewSwitcher**

```tsx
import { useQuery } from '@tanstack/react-query'
import { getWatermarkStatus } from '@/api/watermark'

function PdfPreviewSwitcher({ documentId, versionId, url, title }: { ... }) {
  // userChoice = explicit click; until then the default derives from
  // watermark availability. A doc with no watermarked rendition used to
  // greet the user with the "No watermarked preview" empty state.
  const [userChoice, setUserChoice] = useState<'watermarked' | 'original' | null>(null)
  const statusQ = useQuery({
    queryKey: ['watermark-status', documentId, versionId],
    queryFn: () => getWatermarkStatus(documentId, versionId),
  })
  const wmUnavailable =
    statusQ.data != null &&
    (statusQ.data.status === 'none' || statusQ.data.status === 'failed' || (statusQ.data.page_count ?? 0) <= 0)
  const mode = userChoice ?? (wmUnavailable ? 'original' : 'watermarked')
  // buttons: onClick={() => setUserChoice('watermarked')} etc. — rest unchanged
```

Keep the same queryKey WatermarkedPreview uses if it differs (open WatermarkedPreview.tsx line ~35 and copy its key) so react-query serves both from one fetch.

- [ ] **Step 4: Run test — expect PASS; run the whole viewer test dir**
- [ ] **Step 5: Commit** — `fix(web): PDF preview falls back to Original when watermark is unavailable`

---

### Task 2: Auto-apply threshold default 0.95 → 0.85 (Python + Go mirrors)

**Files:**
- Modify: `services/intelligence/app/tasks/auto_tag.py:59` (DEFAULT_CONFIG)
- Modify: `services/document/internal/repository/tag_suggestion_repo.go:238` (AutoApplyThreshold fallback)
- Test: `services/intelligence/tests/test_auto_tag.py` (add one test)

**Interfaces:** none — constants only. Add a cross-reference comment at BOTH sites: `# keep in sync with services/document/.../tag_suggestion_repo.go defaults` / `// keep in sync with services/intelligence/app/tasks/auto_tag.py DEFAULT_CONFIG`.

- [ ] **Step 1: Write the failing test**

```python
# append to services/intelligence/tests/test_auto_tag.py
def test_default_auto_apply_threshold_is_085():
    """0.95 pushed nearly every AI tag into the pending queue, which read
    as 'tagging is manual'. 0.85 is the agreed default; tenants override
    via auto_tag_config."""
    from app.tasks.auto_tag import DEFAULT_CONFIG
    assert DEFAULT_CONFIG["auto_apply_threshold"] == 0.85
```

- [ ] **Step 2: Run — expect FAIL (0.95 != 0.85)**: `cd services/intelligence && python -m pytest tests/test_auto_tag.py -k threshold_is_085 -v`
- [ ] **Step 3: Change both literals to `0.85`, add the sync comments**
- [ ] **Step 4: Run the Python test file (PASS) and `go build ./services/document/...`; run `go test -race ./services/document/internal/repository/` if it passes without a live DB (integration-tagged tests are excluded by default)**
- [ ] **Step 5: Commit** — `feat(tagging): default auto-apply threshold 0.95 -> 0.85`

---

### Task 3: Dashboard "AI tag suggestions waiting" card

**Files:**
- Create: `web/src/components/intelligence/PendingSuggestionsCard.tsx`
- Modify: `web/src/routes/_authenticated/index.tsx` (render card in the KPI/summary area)
- Test: `web/src/components/intelligence/__tests__/PendingSuggestionsCard.test.tsx`

**Interfaces:**
- Consumes: `listPendingTagSuggestions({ limit: 1 })` from `@/api/intelligence` → `{ suggestions, total }`.
- Produces: `<PendingSuggestionsCard />` — self-contained, renders `null` while loading, on ANY error (covers non-admin 403 for `/admin/tag-suggestions`), or when `total === 0`.

- [ ] **Step 1: Failing test**

```tsx
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { PendingSuggestionsCard } from '@/components/intelligence/PendingSuggestionsCard'
import * as intel from '@/api/intelligence'

vi.mock('@/api/intelligence', () => ({ listPendingTagSuggestions: vi.fn() }))

const qc = () => new QueryClient({ defaultOptions: { queries: { retry: false } } })

it('shows the pending count and links to the review queue', async () => {
  vi.mocked(intel.listPendingTagSuggestions).mockResolvedValue({ suggestions: [], total: 7, limit: 1, offset: 0 })
  render(<QueryClientProvider client={qc()}><PendingSuggestionsCard /></QueryClientProvider>)
  expect(await screen.findByText(/7/)).toBeInTheDocument()
  expect(screen.getByRole('link', { name: /review/i })).toHaveAttribute('href', expect.stringContaining('/admin/tags'))
})

it('renders nothing on error (non-admin 403)', async () => {
  vi.mocked(intel.listPendingTagSuggestions).mockRejectedValue(new Error('403'))
  const { container } = render(<QueryClientProvider client={qc()}><PendingSuggestionsCard /></QueryClientProvider>)
  await new Promise((r) => setTimeout(r, 0))
  expect(container).toBeEmptyDOMElement()
})
```

Note: the route test may need the router Link mocked or the card should use a plain `<a href="/admin/tags">` — look at how other dashboard cards link (index.tsx uses `Link` from TanStack router); for testability without a router provider, use `Link` and wrap the test render in the same test-router helper other route tests use (`web/src/test/` has helpers — check `web/src/test/` first; if none fits, plain `<a>` is acceptable here).

- [ ] **Step 2: Run — FAIL (module missing)**
- [ ] **Step 3: Implement**

```tsx
// PendingSuggestionsCard.tsx
import { useQuery } from '@tanstack/react-query'
import { Sparkles } from 'lucide-react'
import { listPendingTagSuggestions } from '@/api/intelligence'
import { WarmCard } from '@/components/ui/crextio'

export function PendingSuggestionsCard() {
  const q = useQuery({
    queryKey: ['pending-tag-suggestions-count'],
    queryFn: () => listPendingTagSuggestions({ limit: 1 }),
    staleTime: 60_000,
  })
  if (!q.data || q.isError || q.data.total === 0) return null
  return (
    <a href="/admin/tags" className="block" data-testid="pending-suggestions-card">
      <WarmCard padded="md" className="flex items-center gap-3">
        <Sparkles className="h-5 w-5" aria-hidden />
        <div>
          <p className="text-2xl font-semibold">{q.data.total}</p>
          <p className="text-sm text-muted-foreground">AI tag suggestions waiting — review</p>
        </div>
      </WarmCard>
    </a>
  )
}
```

Then render `<PendingSuggestionsCard />` in `index.tsx` directly under the KPI grid (inside the same page container as `<OpenTasksCard />` — match surrounding layout classes).

- [ ] **Step 4: Run tests — PASS; also `npx vitest run src/routes` smoke if a dashboard test exists**
- [ ] **Step 5: Commit** — `feat(web): dashboard card for pending AI tag suggestions`

---

### Task 4: Document card ✨ suggestion badge + one-click review popover

**Files:**
- Modify: `web/src/components/documents/DocumentCard.tsx` (`export function DocumentCard({ doc }: { doc: Document })`)
- Create: `web/src/components/documents/TagSuggestionBadge.tsx`
- Test: `web/src/components/documents/__tests__/TagSuggestionBadge.test.tsx`

**Interfaces:**
- Consumes: `listTagSuggestions(documentId)` and `reviewTagSuggestions(documentId, actions)` from `@/api/intelligence` (`actions: { suggestion_id, action: 'accept' | 'reject' }[]`).
- Produces: `<TagSuggestionBadge documentId={doc.id} />` — renders nothing when zero pending; a "✨ N suggested" pill otherwise; clicking opens a popover listing pending suggestions each with Accept/Reject buttons; on review success it invalidates `['documents']` and `['tag-suggestions', documentId]` query keys.

- [ ] **Step 1: Failing test** — three cases: (a) zero pending → empty render; (b) 2 pending → badge shows "2 suggested"; (c) clicking Accept calls `reviewTagSuggestions(docId, [{suggestion_id: 's1', action: 'accept'}])`. Mock `@/api/intelligence` with `vi.mock`; only suggestions with `status === 'pending'` count. Use `@testing-library/user-event` for the popover click like neighboring component tests do.
- [ ] **Step 2: Run — FAIL**
- [ ] **Step 3: Implement** — query with `staleTime: 60_000`, filter `status === 'pending'`; popover via the same Radix Popover primitives used elsewhere in `web/src/components/ui` (grep `Popover` for the local wrapper); each row: tag name + confidence % + Accept/Reject icon buttons with `aria-label={`Accept tag ${name}`}`. Mount `<TagSuggestionBadge documentId={doc.id} />` in DocumentCard's footer row next to existing badges.
- [ ] **Step 4: Run — PASS; run the documents component test dir**
- [ ] **Step 5: Commit** — `feat(web): AI tag suggestion badge with one-click review on document cards`

---

### Task 5: Dashboard smart upload dialog

**Files:**
- Create: `web/src/components/documents/DashboardUploadDialog.tsx`
- Modify: `web/src/routes/_authenticated/index.tsx` (QUICK_ACTIONS entry + QuickActions renderer)
- Test: `web/src/components/documents/__tests__/DashboardUploadDialog.test.tsx`

**Interfaces:**
- Consumes: `getWorkspaces(): Promise<Workspace[]>`, `getFolders(workspaceId, parentId?): Promise<Folder[]>` (`@/api/workspaces`); `predictFiling({ filename, mime_type, workspace_id }) → { prediction_id, suggested_folder?, suggested_tags }` and `sendFilingFeedback` (`@/api/predictiveFiling`); `useUpload(workspaceId?, folderId?)` returning `{ uploadFiles(files, decisions?, onComplete?) }` — pass tags via the `FilingDecision` array (same type `useUpload` already imports; open `web/src/hooks/useUpload.ts` top for the exact import path and shape: it carries `folder_id` and `tags`).
- Produces: `<DashboardUploadDialog open onOpenChange />`; the dashboard Upload card opens it.

- [ ] **Step 1: Failing tests** (behavioral core, mock all API modules + `useUpload`):

```tsx
// key assertions — full arrange with vi.mock('@/api/workspaces'), vi.mock('@/api/predictiveFiling'), vi.mock('@/hooks/useUpload')
it('disables Upload until a file and workspace are chosen', ...)
it('applies the suggested folder when its chip is clicked', async () => {
  // predictFiling resolves { prediction_id: 'p1', classification: {...},
  //   suggested_folder: { id: 'f9', name: 'Invoices', score: 0.92, reason: '' },
  //   suggested_tags: [{ tag: 'invoice', confidence: 0.9 }] }
  // click chip "Invoices" → folder select shows Invoices; upload passes folderId f9
})
it('unticking a suggested tag drops it from the FilingDecision', ...)
it('sends filing feedback after upload', ...)  // sendFilingFeedback called with prediction_id p1
it('predict failure still allows plain upload', ...) // predictFiling rejects; Upload still enabled
```

- [ ] **Step 2: Run — FAIL (module missing)**
- [ ] **Step 3: Implement the dialog**

Structure (use the local Dialog wrapper from `web/src/components/ui/shadcn/dialog` — same import UploadReviewDialog.tsx uses; copy its scaffolding):

```tsx
export function DashboardUploadDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (v: boolean) => void }) {
  const [files, setFiles] = useState<File[]>([])
  const [workspaceId, setWorkspaceId] = useState<string>()
  const [folderId, setFolderId] = useState<string>()
  const [tags, setTags] = useState<string[]>([])           // pre-ticked from prediction, user can untick
  const [prediction, setPrediction] = useState<PredictResponse | null>(null)

  const workspacesQ = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces, enabled: open })
  const foldersQ = useQuery({
    queryKey: ['folders', workspaceId],
    queryFn: () => getFolders(workspaceId!),
    enabled: !!workspaceId,
  })
  const { uploadFiles } = useUpload(workspaceId, folderId)

  // Predict on first file + workspace (spec: first file governs the batch).
  useEffect(() => {
    if (!files[0] || !workspaceId) return
    predictFiling({ filename: files[0].name, mime_type: files[0].type, workspace_id: workspaceId })
      .then((p) => { setPrediction(p); setTags(p.suggested_tags.map((t) => t.tag)) })
      .catch(() => setPrediction(null))   // non-blocking by design
  }, [files[0]?.name, workspaceId])

  const onUpload = async () => {
    const decision = prediction || tags.length
      ? [{ prediction_id: prediction?.prediction_id, folder_id: folderId, tags } as FilingDecision]
      : undefined
    await uploadFiles(files, decision && files.map((_, i) => (i === 0 ? decision[0] : null)))
    if (prediction?.suggested_folder) {
      sendFilingFeedback({
        prediction_id: prediction.prediction_id,
        class_accepted: true,
        folder_accepted: folderId === prediction.suggested_folder.id,
      }).catch(() => {})
    }
    onOpenChange(false)
  }
  // render: dropzone (input type=file multiple + onDrop), workspace <Select>,
  // folder <Select> (root default), suggested-folder chip button, tag chips
  // with toggle, success note: "AI is analyzing — more tags may be suggested shortly."
}
```

Match `FilingDecision`'s real fields when writing this (open the type; `sendFilingFeedback`'s exact input fields too — the API file defines them; adjust the call accordingly). If `useUpload` already POSTs feedback when decisions carry `prediction_id` (read the hook body around its `sendFilingFeedback` mention), do NOT double-send — drop the explicit call and assert the decision wiring in tests instead.

- [ ] **Step 4: Rewire the dashboard card**

In `index.tsx`: change the QUICK_ACTIONS Upload entry to `{ icon: Upload, label: 'Upload', action: 'upload' as const, description: 'Pick a workspace and drop a file' }`; extend the `QuickAction` type with optional `action`; in `QuickActions()`, render entries with `action` as a `<button type="button">` (same WarmCard child markup, same classes) that sets `uploadOpen(true)`, and mount `<DashboardUploadDialog open={uploadOpen} onOpenChange={setUploadOpen} />` once. Keyboard/a11y: the button keeps the visible label "Upload".

- [ ] **Step 5: Run dialog tests + full web suite — PASS**
- [ ] **Step 6: Commit** — `feat(web): dashboard smart upload dialog with AI folder/tag suggestions`

---

### Task 6: Viewer compact toolbar + info rail

**Files:**
- Create: `web/src/components/documents/DocumentHeaderToolbar.tsx`
- Modify: `web/src/routes/_authenticated/workspaces/$workspaceId/documents/$documentId.tsx` (header region + right rail; the existing Tags block ~line 691-752 and `TagSuggestionsPanel` move INTO the rail top)
- Test: `web/src/components/documents/__tests__/DocumentHeaderToolbar.test.tsx`

**Interfaces:**
- Produces: `DocumentHeaderToolbar` — pure presentational:

```tsx
export interface DocumentHeaderToolbarProps {
  onDownload: () => void
  onShare: () => void
  onCreateTask: () => void
  onCompare: () => void
  onManageAccess: () => void
  onDeclareRecord: () => void
  onWormLock: () => void
  onVerifyIntegrity: () => void
  integrity: 'verified' | 'not_verified' | 'checking'
  canDeclareRecord: boolean
}
```

Primary icons row (Download, Share, Create task, Compare, Manage access) as icon `Button`s with `aria-label` + Tooltip; overflow `DropdownMenu` ("More actions") containing Declare as record, WORM lock, Verify integrity. Integrity chip rendered next to the title by the route, not the toolbar.

- [ ] **Step 1: Failing test** — renders all five primary actions by accessible name; overflow menu opens and fires `onDeclareRecord`; `integrity="not_verified"` shows no checkmark chip inside toolbar (chip is external). Use userEvent; mock nothing (pure props).
- [ ] **Step 2: Run — FAIL**
- [ ] **Step 3: Implement toolbar component** (Radix DropdownMenu + Tooltip wrappers from `web/src/components/ui`; icons: Download, Share2, CheckSquare, GitCompare, ShieldCheck, MoreHorizontal from lucide).
- [ ] **Step 4: Integrate in the route file**
  - Replace the right-rail button stack (the block containing the Download/Share/Create task/Compare/Manage access buttons — grep `M-4: the sidebar Download` for the top of it, ~line 519) with `<DocumentHeaderToolbar …/>` in the header band; wire the EXISTING handlers (each button's current onClick body becomes the callback — move code, don't rewrite it; the presigned-URL download logic at ~line 529 stays intact inside the route's `handleDownload`).
  - Replace the standalone integrity band with a chip next to the status badge: `✓ verified` (green) / `not verified` (muted) / spinner while checking; clicking the chip calls `onVerifyIntegrity`.
  - Right rail top-to-bottom: Tags block (moved up) + `<TagSuggestionsPanel documentId={documentId} />`, then details (size, language pill — the "English (100%)" pill relocates here, version count), presence ("No one else here"), Comments.
  - Delete the now-empty bands; preview area takes the freed height.
- [ ] **Step 5: Run toolbar tests + FULL web suite (the 1.7k route file has existing tests — they must stay green; fix any that asserted the old DOM structure by updating selectors, not by deleting assertions)**
- [ ] **Step 6: Manual check** — `npm run dev` is already running via `sedoc-web.service`; open a document and confirm: toolbar present, tags visible without scrolling, PDF without watermark shows original.
- [ ] **Step 7: Commit** — `feat(web): compact document viewer header with icon toolbar and info rail`

---

## Self-review notes

- Spec coverage: §1 dialog → Task 5; §2 threshold → Task 2, dashboard card → Task 3, card badge → Task 4; §3 toolbar/rail/preview → Tasks 1 + 6. Multi-file first-file rule encoded in Task 5 Step 3.
- Types cross-checked: `PredictResponse.suggested_folder.id`, `FilingDecision` via useUpload import, `reviewTagSuggestions` action shape, `getFolders(workspaceId, parentId?)`.
- Order matters only for Tasks 3/4 (both touch suggestion queries) and 1 before 6 (same viewer area) — otherwise independent.
