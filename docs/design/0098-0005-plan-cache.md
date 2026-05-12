# Design: Cross-Session Normalized-Query Plan Cache (M0098-0005)

**Status**: accepted  
**Milestone**: M0098-0005  
**Expected gain**: 20–40% reduction in per-transaction CPU for repeated queries

## Problem

Every SQL query goes through `parser.Parse()` + `planner.Plan()` on every call.
For pgbench with 100 connections, the same 8 parameterized queries are parsed
and planned 100 times each at startup (800 redundant plan operations). For
simple-protocol workloads, every transaction re-plans the same SQL.

## Design

### 16-shard plan cache

`internal/server/plancache.go`:

```go
type planCache struct {
    shards [16]planCacheShard
}
type planCacheShard struct {
    mu      sync.RWMutex
    entries map[string]planner.Node // key → cached plan
    order   []string               // FIFO eviction
}
```

- **16 shards** (FNV-1a hash of key & 15) — reduces lock contention for concurrent
  sessions planning different queries
- **32 entries per shard = 512 total** — covers all pgbench queries with headroom
- **FIFO eviction** — oldest entry dropped when shard is full (simple, avoids LRU overhead)
- **Key**: `normalizeCompatSQL(sql)` — lowercase, whitespace-collapsed, no trailing
  semicolons; stable across sessions with minor formatting differences

### Cacheable plans

Only stateless query plans are cached:
- DML: SELECT, INSERT (without ON CONFLICT target), UPDATE, DELETE, MERGE
- Any plan not in `{*planner.DDL, *planner.Transaction, *planner.Copy}`

DDL and Transaction nodes mutate server state; caching them would be incorrect.

### DDL invalidation

After any DDL statement executes (detected via `node.(*planner.DDL)` type assertion
in `commandTagFor` exit path), `planCache.Invalidate()` clears all shards. This
prevents stale schema references from being executed by subsequent queries.

### Integration points

1. **Simple query path** (`dispatch.go`): Before the statement execution loop for
   single-statement queries, check the cache. On miss, plan immediately and cache.
   Pass the pre-built node to `executeOneSimpleStmt` via variadic param.

2. **Extended protocol** (`dispatch_extended.go`): After parsing (single-stmt enforced
   by the protocol), check the cache before `planner.Plan()`. On miss, plan and cache.

3. **Server initialization** (`server.go`): `planCache` allocated in `New()` when
   storage handles are present (`cfg.hasStorage()`).

### Thread safety

- `planCache.Get()` acquires `shard.mu.RLock()` — concurrent reads are parallel.
- `planCache.Put()` acquires `shard.mu.Lock()` — brief exclusive lock per shard.
- `planCache.Invalidate()` acquires each shard's write lock sequentially.
- `planner.Node` instances are immutable trees (no runtime state) — safe to share.

## Files changed

| File | Change |
|------|--------|
| `internal/server/plancache.go` | New: planCache type with Get/Put/Invalidate |
| `internal/server/server.go` | Add `pc *planCache` to Server; init in New() |
| `internal/server/dispatch.go` | Single-stmt cache lookup+store; DDL invalidation; variadic cachedNode in executeOneSimpleStmt |
| `internal/server/dispatch_extended.go` | Cache lookup+store; DDL invalidation |
| `docs/design/README.md` | Index entry |
