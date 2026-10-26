// The dashboard's Ask card sends the question to /ask?q=<text>. The Ask page
// submits it once on arrival and then clears it from the URL, so a reload or
// Back does not ask the same question again.
//
// Unlike the other route tests this one mounts the real route on a real
// router (memory history): the behaviour under test is the interaction
// between the search param, the page's effect and the navigation that clears
// it, which a mocked useSearch/useNavigate cannot exercise.
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
// resolves against it.
async function renderAt(path: string, { strict = false } = {}) {
  const root = createRootRoute()
  const home = createRoute({ getParentRoute: () => root, path: '/', component: () => <p>dashboard</p> })
  const ask = createRoute({
    getParentRoute: () => root,
    path: '/ask',
    validateSearch: routeOptions.validateSearch,
    component: routeOptions.component,
  } as never)
  const router = createRouter({
    routeTree: root.addChildren([home, ask]),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const tree = (
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  const wrapped = strict ? <StrictMode>{tree}</StrictMode> : tree
  let utils!: ReturnType<typeof render>
  await act(async () => { utils = render(wrapped) })
  return Object.assign(router, { rerenderTree: () => utils.rerender(wrapped) })
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

describe('/ask?q=', () => {
  it('asks the question once and shows it in the thread', async () => {
    await renderAt('/ask?q=hello')
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(1))
    expect(vi.mocked(streamQA).mock.calls[0][0]).toMatchObject({ question: 'hello', scope: 'global' })
    expect(within(screen.getByTestId('ask-thread')).getByText('hello')).toBeInTheDocument()
  })

  it('clears q from the URL but keeps the conversation on screen', async () => {
    const router = await renderAt('/ask?q=hello')
    await waitFor(() => expect(router.state.location.search).toEqual({}))
    expect(router.state.location.href).toBe('/ask')
    // The page was not remounted by the navigation: the question is still there.
    expect(within(screen.getByTestId('ask-thread')).getByText('hello')).toBeInTheDocument()
  })

  it('does not ask again when the page re-renders after the URL is cleared', async () => {
    const router = await renderAt('/ask?q=hello')
    await waitFor(() => expect(router.state.location.search).toEqual({}))
    // A re-render of the whole tree, and one of the page itself (typing in the composer).
    await act(async () => { router.rerenderTree() })
    await userEvent.type(screen.getByTestId('ask-input'), 'and more')
    expect(streamQA).toHaveBeenCalledTimes(1)
  })

  it('does not ask again on Back and Forward, because the question never stays in history', async () => {
    const router = await renderAt('/')
    // What the dashboard card does: push /ask?q=...
    await act(async () => { void router.navigate({ to: '/ask', search: { q: 'hello' } }) })
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(router.state.location.href).toBe('/ask'))
    await act(async () => { router.history.back() })
    await screen.findByText('dashboard')
    await act(async () => { router.history.forward() })
    await screen.findByTestId('ask-thread')
    expect(router.state.location.href).toBe('/ask')
    expect(streamQA).toHaveBeenCalledTimes(1)
  })

  it('asks once under StrictMode, which runs mount effects twice in development', async () => {
    await renderAt('/ask?q=hello', { strict: true })
    await waitFor(() => expect(streamQA).toHaveBeenCalled())
    expect(streamQA).toHaveBeenCalledTimes(1)
  })

  it('submits the first 4000 characters of an over-long q', async () => {
    await renderAt(`/ask?q=${'x'.repeat(4100)}`)
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(1))
    expect(calls()[0]).toBe('x'.repeat(MAX_QUESTION_LEN))
  })

  it('trims the question before asking', async () => {
    await renderAt('/ask?q=%20%20hello%20there%20')
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(1))
    expect(calls()[0]).toBe('hello there')
  })

  it('round-trips a numeric-looking question typed into the card', async () => {
    // TanStack quotes a string that would parse as JSON ("2024" -> %222024%22),
    // so it comes back as a string rather than being dropped as a number.
    // Arrives while the page is already mounted, so this also covers the
    // effect reacting to q rather than only reading it on first render.
    const router = await renderAt('/ask')
    await act(async () => { void router.navigate({ to: '/ask', search: { q: '2024' } }) })
    await waitFor(() => expect(streamQA).toHaveBeenCalledTimes(1))
    expect(calls()[0]).toBe('2024')
  })

  it.each([
    ['an empty q', '/ask?q='],
    ['a whitespace-only q', '/ask?q=%20%20%20'],
    ['no q', '/ask'],
  ])('asks nothing for %s', async (_name, path) => {
    const router = await renderAt(path)
    await screen.findByTestId('ask-thread')
    expect(streamQA).not.toHaveBeenCalled()
    expect(within(screen.getByTestId('ask-thread')).queryByRole('article')).toBeNull()
    expect(router.state.location.pathname).toBe('/ask')
  })
})
