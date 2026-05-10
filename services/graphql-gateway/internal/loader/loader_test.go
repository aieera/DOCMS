package loader

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestLoader_BatchesConcurrentLoads(t *testing.T) {
	var calls int32
	fetch := func(_ context.Context, keys []string) (map[string]string, error) {
		atomic.AddInt32(&calls, 1)
		out := map[string]string{}
		for _, k := range keys {
			out[k] = "v:" + k
		}
		return out, nil
	}
	l := New(fetch)
	var wg sync.WaitGroup
	got := make([]string, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := l.Load(context.Background(), "k")
			if err != nil {
				t.Errorf("load: %v", err)
				return
			}
			got[i] = v
		}(i)
	}
	wg.Wait()
	if c := atomic.LoadInt32(&calls); c != 1 {
		t.Fatalf("expected 1 fetcher call (cache hit on dup keys), got %d", c)
	}
	for i, v := range got {
		if v != "v:k" {
			t.Fatalf("got[%d] = %q", i, v)
		}
	}
}

func TestLoader_PrimedKeySkipsFetcher(t *testing.T) {
	var calls int32
	fetch := func(_ context.Context, _ []string) (map[string]string, error) {
		atomic.AddInt32(&calls, 1)
		return nil, nil
	}
	l := New(fetch)
	l.Prime("hot", "cached")
	v, err := l.Load(context.Background(), "hot")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if v != "cached" {
		t.Fatalf("expected cached value, got %q", v)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("primed key should not call fetcher")
	}
}
