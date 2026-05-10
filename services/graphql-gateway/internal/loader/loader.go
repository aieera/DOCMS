// Package loader implements per-request DataLoader batching for
// the GraphQL resolvers (ADR 0074).
//
// The loaders here are intentionally minimal — we don't pull in
// `github.com/graph-gophers/dataloader` or `dataloaden`-generated
// code because the only fan-outs we batch are typed and small
// (≤6 loaders enumerated in the ADR), so the generic interface
// and code-gen of those libraries is overkill.
//
// Two modes:
//
//   - **Cache** (always): a key seen twice in the same request
//     hits the upstream once. The cache lives on the per-request
//     bundle attached to context.Context, so cross-tenant leakage
//     is impossible — the bundle is allocated fresh by the HTTP
//     handler for every inbound request.
//
//   - **Batch** (explicit): when a parent resolver is about to
//     iterate N children that all need the same loader, it calls
//     LoadMany(keys) which fires a single bulk fetch. The
//     subsequent per-child Load(k) calls become cache hits.
//
// We don't try to do the goroutine-window auto-batching trick the
// canonical DataLoader uses. The executor walks the selection set
// serially, so an auto-batch window would either be empty (fire
// immediately, no batching) or arbitrary (sleep N ms, hurt latency).
// The explicit LoadMany pattern is honest about what's batched.
package loader

import (
	"context"
	"sync"

	"github.com/vaultdms/vaultdms/services/graphql-gateway/internal/model"
)

type ctxKey struct{}

// Loaders bundles every per-request DataLoader. main.go attaches
// one of these per HTTP request via WithLoaders; resolvers pull
// it out via FromContext.
type Loaders struct {
	VersionsByDocument    *Loader[string, []*model.Version]
	CommentsByDocument    *Loader[string, []*model.Comment]
	AnnotationsByDocument *Loader[string, []*model.Annotation]
	WorkflowsByDocument   *Loader[string, []*model.WorkflowInstance]
	PermissionsByDocument *Loader[string, *model.DocumentPermissions]
	UserByID              *Loader[string, string]
}

// FromContext returns the request-scoped loader bundle. Returns
// nil if the context wasn't decorated — callers in that case
// should fall back to non-batched fetches.
func FromContext(ctx context.Context) *Loaders {
	v, _ := ctx.Value(ctxKey{}).(*Loaders)
	return v
}

// WithLoaders returns a derived context carrying the bundle.
func WithLoaders(ctx context.Context, l *Loaders) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// Fetcher is the bulk-fetch implementation a Loader holds. It
// receives a deduplicated key list and must return a key-keyed
// map. Missing keys map to the zero value (consistent with how
// gqlgen + dataloaden handle gaps).
type Fetcher[K comparable, V any] func(ctx context.Context, keys []K) (map[K]V, error)

// Loader is a per-request keyed cache backed by a bulk fetcher.
//
// Concurrent Load(k) calls for the same key dedupe to one fetcher
// invocation via the in-flight promise (single-flight). Different
// keys arriving concurrently each fire their own single-key fetch
// — explicit batching goes through LoadMany.
type Loader[K comparable, V any] struct {
	fetch Fetcher[K, V]

	mu       sync.Mutex
	cache    map[K]V
	inflight map[K]*promise[V]
}

type promise[V any] struct {
	done  chan struct{}
	value V
	err   error
}

// New returns a Loader bound to a fetcher.
func New[K comparable, V any](fetch Fetcher[K, V]) *Loader[K, V] {
	return &Loader[K, V]{
		fetch:    fetch,
		cache:    map[K]V{},
		inflight: map[K]*promise[V]{},
	}
}

// Load returns the value for one key, hitting the cache first.
// Concurrent calls for the same key share a single fetcher
// invocation via single-flight.
func (l *Loader[K, V]) Load(ctx context.Context, key K) (V, error) {
	l.mu.Lock()
	if v, ok := l.cache[key]; ok {
		l.mu.Unlock()
		return v, nil
	}
	if p, ok := l.inflight[key]; ok {
		l.mu.Unlock()
		return waitPromise(ctx, p)
	}
	p := &promise[V]{done: make(chan struct{})}
	l.inflight[key] = p
	l.mu.Unlock()

	go func() {
		results, err := l.fetch(ctx, []K{key})
		l.mu.Lock()
		var v V
		if results != nil {
			if x, ok := results[key]; ok {
				v = x
				l.cache[key] = x
			}
		}
		p.value = v
		p.err = err
		close(p.done)
		delete(l.inflight, key)
		l.mu.Unlock()
	}()

	return waitPromise(ctx, p)
}

// LoadMany fetches a batch in one fetcher call. Subsequent
// per-key Load() calls become cache hits. Returns the results
// map directly; callers iterate it themselves rather than
// blocking N times.
func (l *Loader[K, V]) LoadMany(ctx context.Context, keys []K) (map[K]V, error) {
	if len(keys) == 0 {
		return map[K]V{}, nil
	}
	// Filter out cached keys so the upstream call only carries the
	// misses.
	l.mu.Lock()
	out := map[K]V{}
	misses := keys[:0:0]
	for _, k := range keys {
		if v, ok := l.cache[k]; ok {
			out[k] = v
			continue
		}
		misses = append(misses, k)
	}
	l.mu.Unlock()

	if len(misses) == 0 {
		return out, nil
	}
	fetched, err := l.fetch(ctx, dedupe(misses))
	if err != nil {
		return out, err
	}
	l.mu.Lock()
	for k, v := range fetched {
		l.cache[k] = v
		out[k] = v
	}
	l.mu.Unlock()
	return out, nil
}

// Prime seeds the cache with a known value so a downstream Load()
// returns it without firing the fetcher. Used when a parent
// resolver has already paid the round-trip cost.
func (l *Loader[K, V]) Prime(key K, value V) {
	l.mu.Lock()
	l.cache[key] = value
	l.mu.Unlock()
}

func waitPromise[V any](ctx context.Context, p *promise[V]) (V, error) {
	select {
	case <-ctx.Done():
		var zero V
		return zero, ctx.Err()
	case <-p.done:
		return p.value, p.err
	}
}

func dedupe[K comparable](in []K) []K {
	seen := map[K]struct{}{}
	out := make([]K, 0, len(in))
	for _, k := range in {
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}
