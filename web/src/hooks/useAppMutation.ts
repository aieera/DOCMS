// useAppMutation — thin wrapper over react-query's useMutation that
// injects a sensible default onError (toast the backend's error
// message via readErrorMessage) so mutation call sites can omit the
// boilerplate without losing user-facing feedback.
//
// Wave 5 pattern 1: the audit found ~40% of useMutation call sites
// had no onError, leaving 403 / 423 / 5xx failures silent. Rather
// than write the same `onError: (e) => toast.error(readErrorMessage
// (e) ?? '<fallback>')` in 60+ places, this wrapper makes the
// default the explicit no-op-to-write case.
//
// Invariants (Wave 5 pattern 1 acceptance criteria — verified by
// the matching test file):
//   1. If the caller passes onError, the caller's handler is used
//      verbatim. The wrapper NEVER fires its default toast in
//      addition to the override — they're mutually exclusive.
//   2. onSuccess, onMutate, onSettled, mutationKey, mutationFn, and
//      every other useMutation option pass through untouched. This
//      wrapper exists to fix the onError gap and nothing else;
//      query-invalidation, optimistic updates, etc. are out of
//      scope and must survive every migration.
//   3. defaultErrorMessage is an optional fallback used when
//      readErrorMessage(e) returns null (server didn't send a
//      parsable error envelope). Callers should pass an action-
//      specific string ("Could not save settings", "Move failed")
//      so the toast is more useful than a generic "Something went
//      wrong".

import {
  useMutation,
  type UseMutationOptions,
  type UseMutationResult,
} from '@tanstack/react-query'
import { toast } from 'sonner'
import { readErrorMessage } from '@/api/client'

export interface UseAppMutationOptions<
  TData = unknown,
  TError = unknown,
  TVariables = void,
  TContext = unknown,
> extends UseMutationOptions<TData, TError, TVariables, TContext> {
  /**
   * Fallback toast message used when the backend didn't send a
   * recognizable error envelope and readErrorMessage(e) returns
   * null. Ignored when the caller passes their own onError.
   */
  defaultErrorMessage?: string
}

export function useAppMutation<
  TData = unknown,
  TError = unknown,
  TVariables = void,
  TContext = unknown,
>(
  options: UseAppMutationOptions<TData, TError, TVariables, TContext>,
): UseMutationResult<TData, TError, TVariables, TContext> {
  const { onError, defaultErrorMessage, ...rest } = options
  // The mutually-exclusive choice between caller-provided onError
  // and the default is intentional: a wrapper that ALWAYS toasted
  // would double-toast every override site. If the caller wants
  // both their custom logic AND the toast, they can call
  // readErrorMessage themselves inside their handler — which is
  // exactly the pattern most existing onError'd sites already use.
  return useMutation({
    ...rest,
    onError:
      onError ??
      ((e: TError) => {
        toast.error(
          readErrorMessage(e) ?? defaultErrorMessage ?? 'Something went wrong',
        )
      }),
  })
}
