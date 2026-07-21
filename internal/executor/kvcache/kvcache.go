// Package kvcache is a byte-budgeted, LRU-evicting key/value store for
// per-statement executor caches (correlated-subquery result caches
// today; the hashed-SubPlan tables and a future Memoize operator reuse
// it — correlated-subquery-planning design bundle, D4.5/D6.4).
//
// Concurrency: none. A Cache (and the Budget it draws from) belongs to
// one statement's Context and is touched only by the single goroutine
// executing that statement, the same contract as Context.SubPlanStats.
// There are deliberately no locks.
package kvcache

import "container/list"

// Budget is a byte allowance shared by every cache-like structure of
// one statement, so "the result caches" as a class stay under one cap
// (ch.06 D6.4: WorkMem/4) instead of each store claiming its own.
//
// A limit <= 0 means unlimited — the ch.06 D6.4 contract for
// WorkMem == 0 ("unlimited", never a silent fallback constant).
type Budget struct {
	limit int64
	used  int64
}

// NewBudget returns a budget capped at limit bytes; limit <= 0 means
// unlimited.
func NewBudget(limit int64) *Budget { return &Budget{limit: limit} }

// Unlimited reports whether the budget enforces no cap.
func (b *Budget) Unlimited() bool { return b.limit <= 0 }

// Used returns the bytes currently reserved against the budget.
func (b *Budget) Used() int64 { return b.used }

// Limit returns the configured cap (<= 0 = unlimited).
func (b *Budget) Limit() int64 { return b.limit }

// Reserve claims n bytes. It reports false — reserving nothing — when
// the claim would push usage over the cap.
func (b *Budget) Reserve(n int64) bool {
	if b.limit > 0 && b.used+n > b.limit {
		return false
	}
	b.used += n
	return true
}

// Release returns n bytes to the budget.
func (b *Budget) Release(n int64) {
	b.used -= n
	if b.used < 0 {
		b.used = 0
	}
}

type entry struct {
	key  string
	val  any
	size int64
}

// Cache is an LRU map drawing from a (possibly shared) Budget.
type Cache struct {
	budget    *Budget
	ll        *list.List // front = most recently used
	m         map[string]*list.Element
	bytes     int64
	evictions int64
}

// New returns a cache with its own private budget of budgetBytes
// (<= 0 = unlimited).
func New(budgetBytes int64) *Cache { return NewShared(NewBudget(budgetBytes)) }

// NewShared returns a cache drawing from a shared budget. Eviction is
// local: a cache under pressure evicts its own least-recently-used
// entries only, never a sibling's.
func NewShared(b *Budget) *Cache {
	return &Cache{budget: b, ll: list.New(), m: make(map[string]*list.Element)}
}

// Get returns the value for key and marks it most recently used.
func (c *Cache) Get(key string) (any, bool) {
	el, ok := c.m[key]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*entry).val, true
}

// Put stores val under key, charging sizeBytes to the budget. When the
// budget is exceeded it evicts this cache's LRU entries until the new
// entry fits; if it still cannot fit (larger than the whole remaining
// allowance) the entry is simply not stored — callers fall back to
// recomputation, never to an error.
func (c *Cache) Put(key string, val any, sizeBytes int64) {
	if el, ok := c.m[key]; ok {
		e := el.Value.(*entry)
		c.budget.Release(e.size)
		c.bytes -= e.size
		c.ll.Remove(el)
		delete(c.m, key)
	}
	for !c.budget.Reserve(sizeBytes) {
		if !c.evictOldest() {
			return // nothing left to evict here; entry not stored
		}
	}
	el := c.ll.PushFront(&entry{key: key, val: val, size: sizeBytes})
	c.m[key] = el
	c.bytes += sizeBytes
}

func (c *Cache) evictOldest() bool {
	el := c.ll.Back()
	if el == nil {
		return false
	}
	e := el.Value.(*entry)
	c.ll.Remove(el)
	delete(c.m, e.key)
	c.budget.Release(e.size)
	c.bytes -= e.size
	c.evictions++
	return true
}

// Clear drops every entry, returning their bytes to the budget.
// Clearing is not eviction: the Evictions counter is unchanged.
func (c *Cache) Clear() {
	for el := c.ll.Front(); el != nil; el = el.Next() {
		e := el.Value.(*entry)
		c.budget.Release(e.size)
	}
	c.ll.Init()
	clear(c.m)
	c.bytes = 0
}

// Bytes returns the bytes currently held by this cache.
func (c *Cache) Bytes() int64 { return c.bytes }

// Len returns the number of entries.
func (c *Cache) Len() int { return len(c.m) }

// Evictions returns how many entries budget pressure has evicted.
func (c *Cache) Evictions() int64 { return c.evictions }

// BudgetLimit exposes the byte budget this cache evicts against
// (<= 0 = unlimited). Lets a caller detect that a single in-flight
// entry can never fit and abandon it early (the Memoize overflow
// case) instead of buffering unboundedly before a doomed Put.
func (c *Cache) BudgetLimit() int64 { return c.budget.Limit() }
