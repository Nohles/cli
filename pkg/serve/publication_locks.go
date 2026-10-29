package serve

import (
	"context"
	"sync"
)

// publicationLocks serialize cold opens of one local publication identity.
// Waiters remain cancellable; unrelated publications open independently. Keys
// exist only while owned or awaited, so a large library does not grow a lock
// registry forever. Session URLs retain their per-request rights/fetch behavior.
type publicationLocks struct {
	mu      sync.Mutex
	entries map[string]*publicationLock
}
type publicationLock struct {
	token chan struct{}
	users int
}

func (g *publicationLocks) acquire(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	if g.entries == nil {
		g.entries = make(map[string]*publicationLock)
	}
	entry := g.entries[key]
	if entry == nil {
		entry = &publicationLock{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		g.entries[key] = entry
	}
	entry.users++
	g.mu.Unlock()
	drop := func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		entry.users--
		if entry.users == 0 {
			delete(g.entries, key)
		}
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-entry.token:
		if err := ctx.Err(); err != nil {
			entry.token <- struct{}{}
			drop()
			return nil, err
		}
		var once sync.Once
		return func() { once.Do(func() { entry.token <- struct{}{}; drop() }) }, nil
	}
}
