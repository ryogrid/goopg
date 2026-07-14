package server

// plancache.go — cross-session normalized-query plan cache. M0098-0005.
//
// Architecture:
//   - 16 shards, each holding an RWMutex + map[string]planner.Node.
//   - Max 32 entries per shard = 512 total entries.
//   - Key: planCacheKey(sql, dbOid) — normalizeCompatSQL(sql) (lowercase,
//     whitespace-collapsed, trailing-semicolon stripped) prefixed with the
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

	"github.com/goopg/goopg/internal/planner"
)

const (
	planCacheNumShards    = 16
	planCacheMaxPerShard  = 32 // 16 * 32 = 512 total entries
)

// planCache is the server-level cross-session plan cache. M0098-0005.
type planCache struct {
	shards [planCacheNumShards]planCacheShard
}

type planCacheShard struct {
	mu      sync.RWMutex
	entries map[string]planner.Node
	// order tracks insertion order for simple FIFO eviction.
	order []string
}

func newPlanCache() *planCache {
	pc := &planCache{}
	for i := range pc.shards {
		pc.shards[i].entries = make(map[string]planner.Node, planCacheMaxPerShard)
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

// Get returns the cached plan for key, or (nil, false) if not present.
func (pc *planCache) Get(key string) (planner.Node, bool) {
	s := &pc.shards[shardIndex(key)]
	s.mu.RLock()
	node, ok := s.entries[key]
	s.mu.RUnlock()
	return node, ok
}

// Put stores node under key. If the shard is full, the oldest entry is evicted.
func (pc *planCache) Put(key string, node planner.Node) {
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
func planCacheIsCacheable(node planner.Node) bool {
	switch node.(type) {
	case *planner.DDL, *planner.Transaction, *planner.Copy:
		return false
	}
	return true
}
