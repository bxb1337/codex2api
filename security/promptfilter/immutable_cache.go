package promptfilter

import (
	"container/list"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/singleflight"
)

// immutableCache 只持有有限个只读版本；淘汰不影响已取得对象的在途请求。
type immutableCache[T any] struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	order    *list.List
	builds   singleflight.Group
	recent   atomic.Pointer[immutableCacheEntry[T]]
}

type immutableCacheEntry[T any] struct {
	key   string
	value T
}

func newImmutableCache[T any](capacity int) *immutableCache[T] {
	return &immutableCache[T]{capacity: capacity, items: make(map[string]*list.Element), order: list.New()}
}

func (c *immutableCache[T]) load(key string) (T, bool) {
	if recent := c.recent.Load(); recent != nil && recent.key == key {
		return recent.value, true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.items[key]; ok {
		c.order.MoveToFront(element)
		entry := element.Value.(*immutableCacheEntry[T])
		c.recent.Store(entry)
		return entry.value, true
	}
	var zero T
	return zero, false
}

func (c *immutableCache[T]) put(key string, value T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := &immutableCacheEntry[T]{key: key, value: value}
	if element, ok := c.items[key]; ok {
		element.Value = entry
		c.order.MoveToFront(element)
		c.recent.Store(entry)
		return
	}
	c.items[key] = c.order.PushFront(entry)
	c.recent.Store(entry)
	for c.order.Len() > c.capacity {
		tail := c.order.Back()
		delete(c.items, tail.Value.(*immutableCacheEntry[T]).key)
		c.order.Remove(tail)
	}
}

func (c *immutableCache[T]) getOrBuild(key string, build func() (T, error)) (T, error) {
	if value, ok := c.load(key); ok {
		return value, nil
	}
	value, err, _ := c.builds.Do(key, func() (any, error) {
		if cached, ok := c.load(key); ok {
			return cached, nil
		}
		built, err := build()
		if err == nil {
			c.put(key, built)
		}
		return built, err
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return value.(T), nil
}
