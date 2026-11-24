package cache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type evictCounter struct{ evicted int }

func (e *evictCounter) OnEvict() { e.evicted++ }

func TestTinyLFUHitsExtendTheIdleDeadline(t *testing.T) {
	now := time.Unix(0, 0)
	c := NewTinyLFU(10, 10*time.Minute)
	c.UseRandomizedTTL(0)
	c.now = func() time.Time { return now }

	value := &evictCounter{}
	c.Set("pub", value)

	// Requests every 8 minutes keep it cached well past the original TTL,
	// as an audiobook does while it plays chapter after chapter.
	for range 5 {
		now = now.Add(8 * time.Minute)
		got, ok := c.Get("pub")
		assert.True(t, ok)
		assert.Same(t, value, got)
	}
	assert.Equal(t, 0, value.evicted)

	// Idle past the TTL: the next access misses and evicts it.
	now = now.Add(10*time.Minute + time.Second)
	_, ok := c.Get("pub")
	assert.False(t, ok)
	assert.Equal(t, 1, value.evicted)
}

func TestTinyLFUDelEvicts(t *testing.T) {
	c := NewTinyLFU(10, time.Minute)
	value := &evictCounter{}
	c.Set("pub", value)
	c.Del("pub")
	_, ok := c.Get("pub")
	assert.False(t, ok)
	assert.Equal(t, 1, value.evicted)
}
