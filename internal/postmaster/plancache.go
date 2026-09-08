package postmaster

// plancache.go — cross-session normalized-query plan cache. M0098-0005.
//
// Architecture:
//   - 16 shards, each holding an RWMutex + map[string]planner.Node.
//   - Max 32 entries per shard = 512 total entries.
//   - Key: planCacheKey(sql, dbOid, fingerprint) — the session's planner
//     context (plannerCacheFingerprint: full PlannerSettings plus the four
//     scan-method toggles) joined to normalizeCompatSQL(sql) (lowercase,
//     whitespace-collapsed, trailing-semicolon stripped) and prefixed with the
//     querying connection's effective table/index namespace oid
//     (catalog.NamespaceDBOid). This matches across sessions sending the
//     same SQL even with minor whitespace differences, AND across
//     connections that read the same namespace (e.g. two "postgres"
//     connections) — but NOT across connections whose LookupTable/LookupIndex
//     calls resolve against different namespaces (M0122-0007 slice 4c):
//     a plan embeds resolved *catalog.Table/*catalog.Index pointers, so a
//     plan cached for one namespace must never satisfy a lookup from another.
//   - DDL invalidation: Invalidate() clears all shards atomically.
//     Called after every DDL statement so stale schema references are
//     never executed.
//   - Thread-safe: shards use sync.RWMutex; reads are lock-free per shard.

import (
	"sync"
	"sync/atomic"

	"github.com/goopg/goopg/internal/optimizer"
)

const (
	planCacheNumShards   = 16
	planCacheMaxPerShard = 32 // 16 * 32 = 512 total entries

	// planCacheDoorkeeperSize is the admission filter's slot count (power of
	// two). See planCache.admit. Sized well above the 512 cache entries so a
	// genuinely repeated statement is unlikely to have its mark evicted by
	// one-shot traffic between two executions.
	planCacheDoorkeeperSize = 8192
	planCacheDoorkeeperMask = planCacheDoorkeeperSize - 1
)

// planCache is the server-level cross-session plan cache. M0098-0005.
type planCache struct {
	shards [planCacheNumShards]planCacheShard
	// doorkeeper is the admission filter (perf-optimize-take3/06 candidate H).
	// Lock-free: one atomic Swap per Put.
	doorkeeper [planCacheDoorkeeperSize]atomic.Uint64
}

type planCacheShard struct {
	mu      sync.RWMutex
	entries map[string]optimizer.Node
	// order tracks insertion order for simple FIFO eviction.
	order []string
}

func newPlanCache() *planCache {
	pc := &planCache{}
	for i := range pc.shards {
		pc.shards[i].entries = make(map[string]optimizer.Node, planCacheMaxPerShard)
		pc.shards[i].order = make([]string, 0, planCacheMaxPerShard)
	}
	return pc
}

// shardIndex returns the shard index for key using FNV-1a hashing.
func shardIndex(key string) int {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return int(h & (planCacheNumShards - 1))
}

// hashKey64 is FNV-1a/64 over the key, used by the admission filter. The
// 32-bit shardIndex hash is deliberately NOT reused: a 32-bit value collides
// often enough at 8192 slots to admit unrelated one-shot keys.
func hashKey64(key string) uint64 {
	const (
		off = 14695981039346656037
		prm = 1099511628211
	)
	h := uint64(off)
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= prm
	}
	return h
}

// admit is a doorkeeper: a key earns a cache slot only on its SECOND Put.
//
// perf-optimize-take3/06 candidate H. Under pgbench's default simple protocol
// every statement arrives with its literals substituted client-side, so
// planCacheKey is unique per execution, every Get misses and every Put then
// took the shard WRITE lock and evicted a live entry. That made planCache.Put
// 66% of all remaining -S mutex delay once the lock-manager fast path landed —
// a write storm on behalf of keys that can never be read back.
//
// Admitting on the second sighting fixes both halves of that. One-shot SQL
// never reaches the write lock at all, and the 512 entries stop being churned
// by literal noise, so genuinely repeated statements survive instead of being
// evicted by traffic that will never hit. A repeated statement is cached from
// its third execution rather than its second — irrelevant for anything hot
// enough to matter.
//
// Both error directions are benign. A hash collision that admits early caches a
// one-shot plan, which is exactly today's behaviour. A mark lost to collision
// costs one extra parse. Marks deliberately SURVIVE Invalidate(): "this SQL has
// been seen before" stays true across DDL, so a hot statement is re-admitted on
// its next execution rather than re-learning.
func (pc *planCache) admit(key string) bool {
	h := hashKey64(key)
	if h == 0 {
		h = 1 // 0 is the never-seen sentinel
	}
	return pc.doorkeeper[h&planCacheDoorkeeperMask].Swap(h) == h
}

// Get returns the cached plan for key, or (nil, false) if not present.
func (pc *planCache) Get(key string) (optimizer.Node, bool) {
	s := &pc.shards[shardIndex(key)]
	s.mu.RLock()
	node, ok := s.entries[key]
	s.mu.RUnlock()
	return node, ok
}

// Put stores node under key. If the shard is full, the oldest entry is evicted.
func (pc *planCache) Put(key string, node optimizer.Node) {
	// Admission filter first: this is the whole point, so it must run BEFORE
	// the shard write lock is taken. Skipping a Put is always safe — the key
	// maps deterministically to the same plan, so a skipped store loses at
	// most one cache hit, never correctness.
	if !pc.admit(key) {
		return
	}
	s := &pc.shards[shardIndex(key)]
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[key]; !exists {
		// Evict oldest entry if at capacity.
		if len(s.order) >= planCacheMaxPerShard {
			oldest := s.order[0]
			s.order = s.order[1:]
			delete(s.entries, oldest)
		}
		s.order = append(s.order, key)
	}
	s.entries[key] = node
}

// Invalidate drops all cached entries. Called after DDL statements to
// prevent stale schema references. M0098-0005.
func (pc *planCache) Invalidate() {
	for i := range pc.shards {
		s := &pc.shards[i]
		s.mu.Lock()
		clear(s.entries)
		s.order = s.order[:0]
		s.mu.Unlock()
	}
}

// planCacheIsCacheable reports whether node is safe to cache across sessions.
// DDL, Transaction, and utility nodes change server state and must never be
// cached.
func planCacheIsCacheable(node optimizer.Node) bool {
	switch node.(type) {
	case *optimizer.DDL, *optimizer.Transaction, *optimizer.Copy:
		return false
	}
	return true
}
