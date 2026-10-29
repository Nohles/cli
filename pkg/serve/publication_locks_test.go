package serve

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/readium/cli/pkg/serve/cache"
	"github.com/stretchr/testify/require"
)

func TestPublicationLockCancellationAndIndependentKeys(t *testing.T) {
	var g publicationLocks
	release, err := g.acquire(t.Context(), "book")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		unlock, err := g.acquire(ctx, "book")
		if err == nil {
			unlock()
		}
		done <- err
	}()
	other, err := g.acquire(t.Context(), "other")
	require.NoError(t, err)
	other()
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	release()
	release() // Cleanup is safe even when an eviction retry releases early.
	g.mu.Lock()
	defer g.mu.Unlock()
	require.Empty(t, g.entries)
}

func TestConcurrentLocalOpensReusePublication(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "book.epub"), buildMiniEPUB(t, "one", "two"), 0600))
	s := NewServer(ServerConfig{}, Remote{LocalDirectory: dir})
	const readers = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan *cache.CachedPublication, readers)
	errs := make(chan error, readers)
	for range readers {
		wg.Go(func() {
			<-start
			cp, err := s.getPublication(authContext("book.epub"))
			if err != nil {
				errs <- err
				return
			}
			results <- cp
		})
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var first *cache.CachedPublication
	count := 0
	for cp := range results {
		if first == nil {
			first = cp
		}
		require.Same(t, first, cp, "same cold publication parsed more than once")
		cp.Release()
		count++
	}
	require.Equal(t, readers, count)
	first.OnEvict()
	require.Empty(t, s.publicationOpens.entries)
}
