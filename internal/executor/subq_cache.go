package executor

import (
	"github.com/goopg/goopg/internal/executor/kvcache"
	"github.com/goopg/goopg/internal/optimizer"
)

// Stage 10 (D4.4/D4.5): the sublink result caches, consolidated onto
// the byte-budgeted kvcache package. See the field comment on Context
// (subqBudget/subqCacheSafe/subqCacheScoped) for the two key families
// and why the scoped store keeps the historical clear-on-depth-change
// guard.

// subqBudgetInit lazily creates the statement's shared cache budget and
// the two result stores. Budget = WorkMem/4 (ch.06 D6.4); WorkMem == 0
// means unlimited — explicitly NOT the hash join's silent 512 MiB
// substitute, which is a sizing gate for a different mechanism.
func (c *Context) subqBudgetInit() {
	if c.subqBudget != nil {
		return
	}
	var limit int64
	if c.WorkMem > 0 {
		limit = c.WorkMem / 4
	}
	c.subqBudget = kvcache.NewBudget(limit)
	c.subqCacheSafe = kvcache.NewShared(c.subqBudget)
	c.subqCacheScoped = kvcache.NewShared(c.subqBudget)
}

// subqCacheStore picks the store for a key family. scoped=false is
// reserved for param-lowered keys (see Context field comment); every
// other caller must pass scoped=true to keep the historical
// clear-on-depth-change semantics, including IsNonCorrelated constant
// keys whose flag is only trustworthy after lowering verified it.
func (c *Context) subqCacheStore(scoped bool) *kvcache.Cache {
	c.subqBudgetInit()
	if !scoped {
		return c.subqCacheSafe
	}
	if c.subqCacheScope != len(c.OuterRows) {
		c.subqCacheScoped.Clear()
		c.subqCacheScope = len(c.OuterRows)
	}
	return c.subqCacheScoped
}

// subqCacheGet looks a sublink result up in the appropriate store.
func (c *Context) subqCacheGet(key string, scoped bool) ([]Datum, bool) {
	if c == nil {
		return nil, false
	}
	v, ok := c.subqCacheStore(scoped).Get(key)
	if !ok {
		return nil, false
	}
	return v.([]Datum), true
}

// subqCachePut stores a sublink result, charging its estimated bytes
// to the shared budget (LRU-evicting on pressure; silently not stored
// when it cannot fit — callers just recompute).
func (c *Context) subqCachePut(key string, scoped bool, vals []Datum) {
	if c == nil {
		return
	}
	c.subqCacheStore(scoped).Put(key, vals, subqResultSize(key, vals))
}

// subqCacheBytes reports the bytes currently reserved by all sublink
// caches (both stores plus hash-map reservations) — visible via the
// shared budget.
func (c *Context) subqCacheBytes() int64 {
	if c == nil || c.subqBudget == nil {
		return 0
	}
	return c.subqBudget.Used()
}

// subqResultSize estimates the resident bytes of one cache entry. The
// datum estimate reuses datumKey — the same serialisation the keys are
// built from — plus a fixed overhead per datum for the Datum struct
// itself and per entry for map/list bookkeeping. An estimate is enough:
// the budget bounds order-of-magnitude blowups (D4.5), it is not an
// allocator.
func subqResultSize(key string, vals []Datum) int64 {
	const perEntryOverhead = 96
	const perDatumOverhead = 48
	n := int64(len(key)) + perEntryOverhead
	for _, d := range vals {
		n += int64(len(datumKey(d))) + perDatumOverhead
	}
	return n
}

// corrSubqHashMapBudget reserves budget for a correlated-subquery hash
// map (subqueryImpl path 2) about to be built. The pre-build check uses
// the planner's row estimate for the inner scan; after the build the
// reservation is reconciled to the map's measured size. Returns
// (reserve os, ok); ok=false means the map would not fit — the caller
// skips building and falls back to the rescan path, which is always
// correct.
func (c *Context) corrSubqHashMapReserve(scan optimizer.Node) (int64, bool) {
	c.subqBudgetInit()
	const perRowEstimate = 64
	est := optimizer.EstimateRows(scan) * perRowEstimate
	if est < perRowEstimate {
		est = perRowEstimate
	}
	if !c.subqBudget.Reserve(est) {
		return 0, false
	}
	return est, true
}

// corrSubqHashMapReconcile swaps the pre-build estimate for the built
// map's measured size. If the measured size no longer fits, the
// reservation is dropped and false is returned — the caller must not
// register the map (it may still answer the current row from it).
func (c *Context) corrSubqHashMapReconcile(reserved int64, hm map[string]Datum) bool {
	c.subqBudget.Release(reserved)
	var actual int64
	const perKeyOverhead = 64
	for k, v := range hm {
		actual += int64(len(k)) + int64(len(datumKey(v))) + perKeyOverhead
	}
	return c.subqBudget.Reserve(actual)
}
