# 0118-0037 — inherit-temp: expansion wiring + TRUNCATE-blocks-parent-scan

Milestone: **M0118-0008** (DDL / VACUUM / maintenance concurrency isolation specs)
Spec: `postgres/src/test/isolation/specs/inherit-temp.spec`
Status: **PROMOTED to `pass` — all 9 permutations byte-for-byte vs PG 18.3.**
Builds on the foundation in [0118-0036](0118-0036-inherit-temp-session-ownership-foundation.md).

## Problem recap

`inherit-temp` builds an inheritance tree `inh_parent` ← `inh_temp_child_s1`
(temp, session s1) and `inh_temp_child_s2` (temp, session s2). In PostgreSQL each
backend owns its own temp namespace, so `s1`'s scan/UPDATE/DELETE/TRUNCATE of the
persistent parent includes its own temp child but **excludes** the other
session's (`RELATION_IS_OTHER_TEMP`). goopg keeps all relations in one shared
catalog and registered every `INHERITS` child against the parent regardless of
session, so `s1_select_p` returned 6 rows where PG returns 4, etc.

The foundation slice (0118-0036) landed the per-session `TempOwner` token and the
shared filter `catalog.AccessibleInheritanceChildren(children, owner)` but wired
nothing into live paths. This slice does the wiring **and** the TRUNCATE blocking
semantics, then promotes the spec.

## What landed

### 1. Expansion-site wiring (RELATION_IS_OTHER_TEMP)

Every data-path inheritance expansion the spec exercises now filters the child
list through `catalog.AccessibleInheritanceChildren(children, owner)`:

| path | site | owner source |
|------|------|--------------|
| SELECT (planner) | `collectInheritanceDescendants` (`planner.go`), called from `planScanRangeVar` | `currentTempOwner(cat)` |
| UPDATE | `operators_storage.go` (`updateOp` scan-table collection) | `sessionTempOwner(o.ctx)` |
| DELETE | `operators_storage.go` (`deleteOp` victim collection) | `sessionTempOwner(o.ctx)` |
| UPDATE … FROM | `operators_storage.go` (FROM-scan targets) | `sessionTempOwner(o.ctx)` |
| DELETE … USING | `operators_storage.go` (USING-scan targets) | `sessionTempOwner(o.ctx)` |
| TRUNCATE | `operators_ddl.go` `truncateTableAndPartitions` + the TRUNCATE…CASCADE expansion | `sessionTempOwner(o.ctx)` |

The executor sites hold a `*Context` so they read the owner directly. The planner
SELECT site has no session identity, so the token is threaded through the catalog
wrapper:

- `catalog.SearchPathCatalog` gained a `TempOwnerToken` field and a
  `CurrentTempOwner() string` method.
- `server.sessionPlanCatalog` / `ctxPlanCatalog` set the token to `"s"+UniqueID()`
  (identical to `executor.sessionTempOwner`).
- `planner.currentTempOwner(cat)` walks the wrapper chain (`Unwrap`, like
  `inMemoryCat`) to find the first `CurrentTempOwner()` carrier; returns `""`
  (legacy single-session behaviour) when none is attached.

`PartitionChildren` is intentionally **not** filtered: PG forbids a temp partition
of a permanent partitioned parent, so a partition child is never a cross-session
temp relation. FK / MERGE / VACUUM inheritance expansion is also out of scope —
those operations are not exercised by inherit-temp or any other `port` spec, and
the FK descendant collector (`allDescendants`) is a shared helper without a
`*Context`; wiring it is a separate bounded follow-up (deferral ledger).

### 2. Cross-session plan-cache bypass (the load-bearing subtlety)

goopg has a cross-session plan cache keyed by SQL text (M0098-0005). With the
wiring above, the plan for `SELECT a FROM inh_parent` is now **session-dependent**
(it expands to a session-specific child set). Without a fix, `s1` plans first,
caches `parent+s1child`, and `s2_select_p` gets a cache **hit** → wrongly scans
`s1`'s child. (This was the actual first failure after wiring: `s2_select_p`
returned `1,2,3,4` instead of `1,2,5,6`.)

Fix: `catalog.InMemory.HasTempInheritanceChildren()` reports whether any
inheritance parent currently has a session-owned temp child (O(1) when there is no
inheritance at all). `server.sessionTempInheritanceActive(cat)` exposes it through
the wrapper chain, and **both** plan-cache paths (simple `dispatch.go` and
extended `dispatch_extended.go`) skip the cache entirely when it is true — every
session then plans fresh against its own token. Temp inheritance is rare, so the
common OLTP cache behaviour is unchanged.

### 3. TRUNCATE-blocks-parent-scan (last two permutations)

The final two permutations assert that `s1` `BEGIN; TRUNCATE inh_parent;` blocks
`s2_select_p` (scanning the parent) until `s1` commits, but does **not** block
`s2_select_c` (scanning s2's own temp child). Two gaps:

- **TRUNCATE took no heavyweight lock at all.** `execTruncate` now acquires an
  `AccessExclusiveLock` on every target relation via `acquireDDLLockTxn` (held to
  commit inside the explicit transaction; no-op in autocommit), mirroring PG
  `ExecuteTruncate` / `AlterTableGetLockLevel`. Same precedent as create-trigger
  (0118-0027) and alter-table-3 (0118-0032).
- **Autocommit scans took no lock**, so `s2_select_p` (a bare autocommit SELECT)
  could not block. `acquireScanReadLockTxn` (the seqscan / index / index-only open
  hook, 0118-0018) was confined to explicit transactions. It now delegates to
  `acquireRelLockMaybeTransient(rel, AccessShareLock)`: held-to-commit inside an
  explicit txn (unchanged), **transient acquire+release** in autocommit so the
  read still parks behind a conflicting `AccessExclusive` and proceeds the instant
  the holder commits, holding nothing itself. ACCESS SHARE conflicts only with
  ACCESS EXCLUSIVE, so absent concurrent DDL the acquire is granted instantly.

`s2_select_c` reads its own temp child directly (no parent scan), so it never
touches `inh_parent`'s lock and proceeds immediately — matching PG.

## Blast radius & gates

The autocommit half of `acquireScanReadLockTxn` is the only hot-path change: every
single-statement autocommit read now does one uncontended `AccessShare`
acquire+release. pgbench's TPC-B variants wrap reads in explicit transactions
(already locked since 0118-0018); only bare autocommit SELECTs (`pgbench -S`) are
newly affected. Measured: pgbench smoke (standard / -N / -S) **0 failed
transactions**; `-S` ~14.7k TPS at 0.136 ms latency — the lock pair is negligible
against the protocol round-trip.

- `TestPort_IsolationInheritTemp` strict (9 perms, byte-for-byte) — **PASS**.
- Full `TestPort_IsolationSuite` — no regressions.
- All dedicated strict `TestPort_Isolation*` (timeouts/create-trigger/
  alter-table-3/vacuum-*/sequence-ddl/reindex-*/multiple-cic/row-lock/SSI/FK/
  predicate/merge/deadlock) — PASS (lock-timing-sensitive specs re-verified).
- `internal/{catalog,config,planner,server,executor}` unit packages — PASS.
- pgbench smoke — PASS (above).

## Oracle

`src/backend/optimizer/util/inherit.c` (`expand_single_inheritance_child`,
`RELATION_IS_OTHER_TEMP`), `src/backend/catalog/pg_inherits.c`
(`find_inheritance_children`), `src/backend/commands/tablecmds.c`
(`ExecuteTruncate`, `AlterTableGetLockLevel`).

## Deferred follow-up (ledger)

FK / MERGE / VACUUM inheritance-expansion RELATION_IS_OTHER_TEMP filtering — not
exercised by any `port` spec; needs the shared `allDescendants` helper to thread a
session owner. Bounded, low-risk, same `AccessibleInheritanceChildren` chokepoint.
