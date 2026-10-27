// How a question reaches the Ask page.
//
// - In-app handoff (the dashboard's Ask card): router history STATE, never the
//   URL. The page asks it once, then replaces the history entry with the state
//   cleared, so a reload, Back or Forward never asks again. A link from outside
//   the app cannot set history state, so it cannot make the page ask anything.
// - `/ask?q=`: only PREFILLS the composer. Nothing is sent until the user
//   presses Send, and q is cleared from the URL after the prefill.
//
// Unlike the other route tests this one mounts the real route on a real
// router (memory history): the behaviour under test is the interaction
// between the location (search and state), the page's effects and the
// navigations that clear them, which a mocked useSearch/useNavigate cannot
// exercise.
import { StrictMode } from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider, createMemoryHistory, createRootRoute, createRoute, createRouter,
} from '@tanstack/react-router'

import { MAX_QUESTION_LEN } from '@/lib/askLimits'

// Keep the real createFileRoute, but give it a throwaway parent: the generated
// `_authenticated` layout (auth guard, shell) is not what is under test. The
// route's own options (validateSearch, component) are what renderAt re-mounts.
vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>()
  return {
    ...actual,
    createFileRoute: () => (opts: Record<string, unknown>) =>
      actual.createRoute({ getParentRoute: () => actual.createRootRoute(), path: '/ask', ...opts } as never),
  }
})

vi.mock('@/api/doc-qa', () => ({
  streamQA: vi.fn(() => Promise.resolve()),
  getAskHistory: vi.fn(() => Promise.resolve({ conversations: [], messages: [] })),
}))
vi.mock('@/api/workspaces', () => ({ getWorkspaces: vi.fn(() => Promise.resolve([])) }))

import { streamQA } from '@/api/doc-qa'
import { Route } from '../_authenticated/ask'

const routeOptions = (Route as unknown as {
  options: {
    validateSearch: (s: Record<string, unknown>) => { q?: string }
    component: () => React.ReactNode
  }
}).options

// A fresh router (and route tree) per test, built from the real route's options.
// The route keeps its id ('/ask'), so `Route.useSearch()` inside the page
// resolves against it. Passing an existing `history` mounts a new router on it,
// which is what a reload of the current entry looks like.
async function renderAt(
  path: string,
  { strict = false, history = createMemoryHistory({ initialEntries: [path] }) }: {
    strict?: boolean
    history?: ReturnType<typeof createMemoryHistory>
  } = {},
) {
  const root = createRootRoute()
  const home = createRoute({ getParentRoute: () => root, path: '/', component: () => <p>dashboard</p> })
  const ask = createRoute({
    getParentRoute: () => root,
    path: '/ask',
    validateSearch: routeOptions.validateSearch,
    component: routeOptions.component,
  } as never)
  const router = createRouter({ routeTree: root.addChildren([home, ask]), history })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const tree = (
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  const wrapped = strict ? <StrictMode>{tree}</StrictMode> : tree
  let utils!: ReturnType<typeof render>
  await act(async () => { utils = render(wrapped) })
  return Object.assign(router, {
    rerenderTree: () => utils.rerender(wrapped),
    unmountTree: () => utils.unmount(),
  })
}

// What the dashboard's Ask card does: push /ask with the question in history state.
async function askInApp(router: Awaited<ReturnType<typeof renderAt>>, question: string) {
  await act(async () => { void router.navigate({ to: '/ask', state: { askQuestion: question } }) })
}

const calls = () => vi.mocked(streamQA).mock.calls.map(([p]) => p.question)

beforeEach(() => {
  vi.mocked(streamQA).mockClear()
})

describe('/ask validateSearch', () => {
  it('keeps a string q and drops anything else', () => {
    expect(routeOptions.validateSearch({ q: 'hello' })).toEqual({ q: 'hello' })
    expect(routeOptions.validateSearch({ q: 42 }).q).toBeUndefined()
    expect(routeOptions.validateSearch({ q: ['a'] }).q).toBeUndefined()
    expect(routeOptions.validateSearch({}).q).toBeUndefined()
  })

  it('cuts q to the question limit', () => {
    const q = routeOptions.validateSearch({ q: 'x'.repeat(4100) }).q
    expect(q).toHaveLength(MAX_QUESTION_LEN)
  })
})

describe('/ask?q= prefills the composer and never asks', () => {
  it('puts q in the composer without calling the API', async () => {
    await renderAt('/ask?q=hello')
    await waitFor(() => expect(screen.getByTestId('ask-input')).toHaveValue('hello'))
    // Give any (wrong) auto-submit the chance to happen.
    await act(async () => { await new Promise((r) => setTimeout(r, 20)) })
    expect(streamQA).not.toHaveBeenCalled()
    expect(within(screen.getByTestId('ask-thread')).queryByText('hello')).toBeNull()
  })

  it('clears q from the URL after the prefill and keeps the text in the composer', async () => {
    const router = await renderAt('/ask?q=hello')
    await waitFor(() => expect(router.state.location.search).toEqual({}))
    expect(router.state.location.href).toBe('/ask')
    expect(screen.getByTestId('ask-input')).toHaveValue('hello')
    expect(streamQA).not.toHaveBeenCalled()
  })

  it('sends the prefilled question only when the user presses Send', async () => {
    await renderAt('/ask?q=hello')
    await waitFor(() => expect(screen.getByTestId('ask-input')).toHaveValue('hello'))
    expect(streamQA).not.toHaveBeenCalled()
    await userEvent.click(screen.getByTestId('ask-send'))
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(1))
    expect(calls()[0]).toBe('hello')
  })

  it('prefills the first 4000 characters of an over-long q', async () => {
    await renderAt(`/ask?q=${'x'.repeat(4100)}`)
    await waitFor(() => expect(screen.getByTestId('ask-input')).toHaveValue('x'.repeat(MAX_QUESTION_LEN)))
    expect(streamQA).not.toHaveBeenCalled()
  })

  it('trims the prefilled question', async () => {
    await renderAt('/ask?q=%20%20hello%20there%20')
    await waitFor(() => expect(screen.getByTestId('ask-input')).toHaveValue('hello there'))
    expect(streamQA).not.toHaveBeenCalled()
  })

  it('prefills a numeric-looking q that arrives while the page is mounted', async () => {
    // TanStack quotes a string that would parse as JSON ("2024" -> %222024%22),
    // so it comes back as a string rather than being dropped as a number.
    const router = await renderAt('/ask')
    await act(async () => { void router.navigate({ to: '/ask', search: { q: '2024' } }) })
    await waitFor(() => expect(screen.getByTestId('ask-input')).toHaveValue('2024'))
    await waitFor(() => expect(router.state.location.search).toEqual({}))
    expect(streamQA).not.toHaveBeenCalled()
  })

  it.each([
    ['an empty q', '/ask?q='],
    ['a whitespace-only q', '/ask?q=%20%20%20'],
    ['no q', '/ask'],
  ])('prefills and asks nothing for %s', async (_name, path) => {
    const router = await renderAt(path)
    await screen.findByTestId('ask-thread')
    expect(streamQA).not.toHaveBeenCalled()
    expect(screen.getByTestId('ask-input')).toHaveValue('')
    expect(router.state.location.pathname).toBe('/ask')
  })
})

describe('/ask in-app handoff (history state)', () => {
  it('asks a question handed over in history state once and shows it in the thread', async () => {
    const router = await renderAt('/')
    await askInApp(router, 'hello')
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(1))
    expect(vi.mocked(streamQA).mock.calls[0][0]).toMatchObject({ question: 'hello', scope: 'global' })
    expect(within(screen.getByTestId('ask-thread')).getByText('hello')).toBeInTheDocument()
    // The question never appears in the URL.
    expect(router.state.location.href).toBe('/ask')
  })

  it('replaces the history entry with the state cleared, keeping the conversation on screen', async () => {
    const router = await renderAt('/')
    await askInApp(router, 'hello')
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(router.state.location.state.askQuestion).toBeUndefined())
    expect(router.history.location.state.askQuestion).toBeUndefined()
    expect(router.state.location.href).toBe('/ask')
    // Replace, not push: Back goes to the dashboard, not to a stale /ask entry.
    expect(router.history.length).toBe(2)
    // The page was not remounted by the navigation: the question is still there.
    expect(within(screen.getByTestId('ask-thread')).getByText('hello')).toBeInTheDocument()
  })

  it('does not ask again when the page re-renders after the state is cleared', async () => {
    const router = await renderAt('/')
    await askInApp(router, 'hello')
    await waitFor(() => expect(router.state.location.state.askQuestion).toBeUndefined())
    // A re-render of the whole tree, and one of the page itself (typing in the composer).
    await act(async () => { router.rerenderTree() })
    await userEvent.type(screen.getByTestId('ask-input'), 'and more')
    expect(streamQA).toHaveBeenCalledTimes(1)
  })

  it('does not ask again on reload, because the entry no longer carries the question', async () => {
    const history = createMemoryHistory({ initialEntries: ['/'] })
    const router = await renderAt('/', { history })
    await askInApp(router, 'hello')
    await waitFor(() => expect(router.state.location.state.askQuestion).toBeUndefined())
    router.unmountTree()
    // A reload: a new router and page mounted on the same history entry.
    await renderAt('/ask', { history })
    await screen.findByTestId('ask-thread')
    await act(async () => { await new Promise((r) => setTimeout(r, 20)) })
    expect(streamQA).toHaveBeenCalledTimes(1)
  })

  it('does not ask again on Back and Forward', async () => {
    const router = await renderAt('/')
    await askInApp(router, 'hello')
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(router.state.location.state.askQuestion).toBeUndefined())
    await act(async () => { router.history.back() })
    await screen.findByText('dashboard')
    await act(async () => { router.history.forward() })
    await screen.findByTestId('ask-thread')
    expect(router.state.location.href).toBe('/ask')
    expect(streamQA).toHaveBeenCalledTimes(1)
  })

  it('asks once under StrictMode, which runs mount effects twice in development', async () => {
    const router = await renderAt('/', { strict: true })
    await askInApp(router, 'hello')
    await waitFor(() => expect(streamQA).toHaveBeenCalled())
    await waitFor(() => expect(router.state.location.state.askQuestion).toBeUndefined())
    expect(streamQA).toHaveBeenCalledTimes(1)
  })

  it('asks a second handed-over question that arrives while the page is still mounted', async () => {
    const router = await renderAt('/')
    await askInApp(router, 'first')
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(1))
    // The first answer has finished streaming (the scope selector is locked while it streams).
    await waitFor(() => expect(screen.getByTestId('ask-scope')).not.toBeDisabled())
    await askInApp(router, 'second')
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(2))
    expect(calls()).toEqual(['first', 'second'])
  })

  it('waits for a streaming answer, then asks a question handed over mid-stream', async () => {
    let finish: () => void = () => {}
    vi.mocked(streamQA).mockImplementationOnce(() => new Promise<void>((r) => { finish = r }))
    const router = await renderAt('/')
    await askInApp(router, 'first')
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(1))
    await askInApp(router, 'second')
    await act(async () => { await new Promise((r) => setTimeout(r, 20)) })
    expect(streamQA).toHaveBeenCalledTimes(1)
    await act(async () => { finish() })
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(2))
    expect(calls()).toEqual(['first', 'second'])
  })

  it('asks the same text again when it is handed over a second time', async () => {
    // The once-guard is per handoff, not per text.
    const router = await renderAt('/')
    await askInApp(router, 'again')
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(router.state.location.state.askQuestion).toBeUndefined())
    await askInApp(router, 'again')
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(2))
  })

  it('asks the first 4000 characters of an over-long handed-over question, trimmed', async () => {
    const router = await renderAt('/')
    await askInApp(router, `  ${'y'.repeat(4100)}`)
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(1))
    expect(calls()[0]).toBe('y'.repeat(MAX_QUESTION_LEN))
  })

  it('asks nothing for a whitespace-only handed-over question', async () => {
    const router = await renderAt('/')
    await askInApp(router, '   ')
    await screen.findByTestId('ask-thread')
    await act(async () => { await new Promise((r) => setTimeout(r, 20)) })
    expect(streamQA).not.toHaveBeenCalled()
  })
})
