package cache

// Originally from https://github.com/go-redis/cache/blob/v8.4.3/local.go
// Modified to store interface{} instead of []byte

import (
	"sync"
	"time"

	"math/rand/v2"

	"github.com/vmihailenco/go-tinylfu"
)

type Evictable interface {
	OnEvict()
}

type LocalCache interface {
	Set(key string, data Evictable)
	Get(key string) (Evictable, bool)
	Del(key string)
}

// TinyLFU holds parsed publications. Its TTL is an idle timeout: every hit
// pushes the expiry back, so a publication in steady use (an audiobook
// playing chapter after chapter) is never re-parsed mid-session. Expiry is
// only checked on access, as before; size eviction still applies.
type TinyLFU struct {
	mu     sync.Mutex
	lfu    *tinylfu.T
	ttl    time.Duration
	offset time.Duration
	now    func() time.Time
}

// slidingEntry wraps a cached value with its own idle deadline. The
// tinylfu item itself never expires; Get enforces this deadline instead.
type slidingEntry struct {
	value    Evictable
	ttl      time.Duration
	expireAt time.Time
}

var _ LocalCache = (*TinyLFU)(nil)

func NewTinyLFU(size int, ttl time.Duration) *TinyLFU {
	const maxOffset = 10 * time.Second

	offset := min(ttl/10, maxOffset)

	return &TinyLFU{
		lfu:    tinylfu.New(size, 100000),
		ttl:    ttl,
		offset: offset,
		now:    time.Now,
	}
}

func (c *TinyLFU) UseRandomizedTTL(offset time.Duration) {
	c.offset = offset
}

func (c *TinyLFU) Set(key string, b Evictable) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ttl := c.ttl
	if c.offset > 0 {
		ttl += time.Duration(rand.Int64N(int64(c.offset)))
	}

	entry := &slidingEntry{value: b, ttl: ttl, expireAt: c.now().Add(ttl)}
	c.lfu.Set(&tinylfu.Item{
		Key:   key,
		Value: entry,
		OnEvict: func() {
			b.OnEvict()
		},
	})
}

func (c *TinyLFU) Get(key string) (Evictable, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	val, ok := c.lfu.Get(key)
	if !ok {
		return nil, false
	}

	entry := val.(*slidingEntry)
	now := c.now()
	if now.After(entry.expireAt) {
		c.lfu.Del(key)
		return nil, false
	}
	entry.expireAt = now.Add(entry.ttl)

	return entry.value, true
}

func (c *TinyLFU) Del(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lfu.Del(key)
}
