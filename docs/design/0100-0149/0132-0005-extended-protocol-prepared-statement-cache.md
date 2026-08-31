# 0132-0005 — Extended-protocol prepared-statement cache

**Milestone:** M0132 (explicit transactions across the extended query protocol).
**Slice:** M0132-S13 (prepared-statement verification + `-M prepared` vs simple A/B).
**Status:** design (this document) → implemented in the same commit.

## 1. Problem

S13's A/B gate asserts `-M prepared` pgbench TPS exceeds simple-mode TPS. S11's
same-HEAD measurement (`analysis/perf-optimize3/runs/m0132s11_prep_317fb002/`)
found it does not — prepared `-N` is 8,781 vs simple 10,158 (−13.6%), prepared
`-S` 72,857 vs simple 93,738 (−22.3%). The O-XP-1 profile located the shortfall
in the extended message loop: `describeViaPlanner` 13.4% cum on `-S` and
`parser.Parse` 6.2% cum on `-N`. goopg had **no prepared-statement cache** in
the sense PG has one: every `Execute` re-parses its query, and every `Describe`
re-parses *and* re-plans it.

This is a pure-overhead gap, not a missing transaction feature: the per-Execute
auto-commit was already retired by S2–S8 (fsync is back to ~0.32/txn per the
S11 fsync probe). A cross-session **plan** cache (`s.pc`, M0098-0005,
`internal/server/plancache.go`) already exists and is keyed by normalized query
text + namespace OID; the work below closes the two paths that bypass it.

## 2. What is cached (and what is not)

| artifact | cached today? | this change |
|---|---|---|
| planned `planner.Node` | yes — `s.pc`, cross-session, DDL-invalidated | unchanged |
| parsed `parser.Stmt` (Execute) | **no** — `parser.Parse` per Execute | skip parse on plan-cache hit |
| Describe result (RowDescription) | **no** — `describeViaPlanner` per Describe | read from `s.pc` |

The parsed AST is deliberately **not** cached. `planner.Plan` (via
`planStmt` → `analyzer.Analyze` and the `rewrite*DefaultMarkers` /
`rewriteIndirectionStarTargets` passes) mutates its `parser.Stmt` argument in
place, so a cached AST re-fed to `Plan` on a second execution would be
re-analyzed into a corrupt tree. Skipping `parser.Parse` *when the plan is
already cached* is safe and equivalent: the plan cache holds the finished node,
and the only consumers of the parse result — the LISTEN/NOTIFY/UNLISTEN
intercept (`notifyStmtTag`), the empty/multi-statement checks, and the
syntax-error DDL/noop bypasses — are all only reachable on a cache miss,
because none of those statements are ever cacheable (see §3).

## 3. Correctness argument for skipping parse on a cache hit

`planCacheIsCacheable` excludes `*planner.DDL`, `*planner.Transaction`, and
`*planner.Copy`. NOTIFY/LISTEN/UNLISTEN are intercepted by `notifyStmtTag`
*before* `planner.Plan`, so they never reach `s.pc.Put` and therefore never
produce a cache entry. A multi-statement query is rejected (`len(stmts) > 1`)
before planning. So a cache **hit** implies: the query text already parsed as
exactly one statement, it is not a DDL/transaction/copy/notify node, and the
plan is valid for the current namespace. Skipping the parse, the notify check,
and the syntax-error bypass on a hit therefore preserves behavior exactly —
the only observable difference is the removed parse cost.

DDL invalidation is unchanged: `executeExtendedQueryViaExecutor` already calls
`s.pc.Invalidate()` after a DDL node, so the next Execute/Describe re-parses
and re-plans against the new schema.

## 4. Design

### 4.1 Execute path — skip parse on plan-cache hit (`dispatch_extended.go`)

`executeExtendedQueryViaExecutor` currently runs `parser.Parse(query)` before
the plan-cache lookup. The cache lookup is hoisted ahead of the parse:

```
connDBOid := resolveConnDBOid(catalog, dbName)
node, cacheHit := s.pc.Get(planCacheKey(query, connDBOid))   // guarded as today
if !cacheHit {
    stmts, err := parser.Parse(query)         // parse only on miss
    ... syntax-error DDL/noop bypass ...      // unchanged
    ... empty / multi-statement checks ...    // unchanged
    ... notifyStmtTag intercept ...           // unchanged
    node = planner.Plan(stmt, sessionPlanCatalog(...))  // unchanged
    s.pc.Put(key, node) if cacheable
}
// rest of the function consumes `node` unchanged
```

Statement logging (`logStatement`/`logDuration`) is preserved on both branches:
the miss branch logs exactly where it logged before (after a successful parse),
and the hit branch logs the same statement, so a cache-hit Execute is still
logged — no logging behavior change.

### 4.2 Describe path — read the plan from `s.pc` (`extended.go`)

`describeViaPlanner` re-parses+re-plans every call. It now reads the cached
node and derives `node.Output()` from it, falling back to a fresh parse+plan on
a miss — the same non-fatal error contract as before (`nil, false` → NoData):

```
describeViaPlanner(query, sess, connDBOid):
    node := s.pc.Get(planCacheKey(query, connDBOid))        // guarded as Execute
    if miss: node = planner.Plan(parser.Parse(query), sessionPlanCatalog(...))
    schema := node.Output()
    schema == nil → (nil, true)      // NoData (write/DDL/txn)
    else → field descriptions
```

Two consequences, both intentional:

1. **Describe now plans on the same catalog Execute does** (`sessionPlanCatalog`
   instead of the bare `s.cfg.Catalog` the Describe path used before). This is a
   fix, not a change of semantics: a Describe must describe the plan the
   following Execute will actually run, and the previous divergence (search-path
   / `enable_seqscan` / temp-owner overrides applied on Execute but not Describe)
   meant the two could disagree on the output schema for a session with those
   overrides. Sharing the cache key forces them onto the same catalog.
2. **Describe and Execute share cache entries.** This is what makes the `-S`
   read-path saving real (pgbench's `PQexecPrepared` sends a Describe Portal per
   iteration) and what gives Describe the plan cache's existing DDL invalidation
   for free — no new staleness mechanism is introduced.

The fast-path Describe cases (`SELECT 1`, `SHOW`, `SET`, …) that never reach
`describeViaPlanner` are unchanged.

### 4.3 Threading

`handleDescribeFrame` gains a `sess *config.SessionRegistry` parameter (the
message loop already has it, `server.go:1742` shows the sibling `Execute`
handler already receives it); `connDBOid` is derived from `state.DBName`
(already on `extendedState`), so no new state is stored on the connection.

## 5. Risks / non-goals

- **This is not a full PG `plancache`.** PG caches per-prepared-statement
  generic/custom plans with `plan_cache_mode` heuristics and per-plan
  invalidation by `CacheInvalidateRelcache`. goopg's single cross-session cache
  keyed by (normalized query, namespace) is coarser: two sessions in the same
  database but different `search_path` share an entry. That is the pre-existing
  M0098-0005 contract, unchanged here.
- **The A/B may still not flip `-N`.** Removing the parse recovers ~6.2% of the
  `-N` CPU; the residual prepared-vs-simple gap is protocol framing
  (Parse/Bind/Describe/Execute/Sync vs one Query) plus `TxnMgr.Begin`/snapshot
  (~0.7% per S11), which caching cannot remove. Measured, not assumed — see §7.
- **Binary result formats** remain unsupported (0A000), which S8 already
  ledger'd; out of scope.

## 6. Tests

- `internal/server/extended_prepared_cache_test.go` — a Parse/Bind/Describe/
  Execute round-trip that runs the same prepared statement many times and
  asserts identical results (the cache is exercised by construction), plus a
  DDL-invalidation test: prepare + execute, run `CREATE TABLE`, re-execute,
  assert the plan was re-derived (correct result, not a stale pointer).
- S13's own gates: `scripts/tpch-spotcheck.sh` (Q12=2/Q13=35), the pgbench
  smoke, and the `-M prepared` vs simple A/B recorded in `analysis/`.

## 7. Measurement contract

Re-run the S11 A/B at the new HEAD (same harness, `-N`/`-S`, `-M prepared` vs
simple). Record TPS + latency in `analysis/perf-optimize3/runs/m0132s13_ab_<sha>/`.
If prepared > simple, S13's assertion is discharged and M0132 closes. If the
`-S` case is still short, doc 09's O-XP-1 `-S` read-path profile is the
explicit escape hatch; if `-N` is still short after parse removal, the residual
is protocol framing and is recorded with a deferral-ledger row.
