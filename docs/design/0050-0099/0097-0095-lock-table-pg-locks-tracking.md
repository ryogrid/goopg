# M0097-0095: LOCK TABLE pg_locks Tracking + View Schema Improvements

## Problem

The `lock.sql` regress test had 83 diff lines. The root causes were:
1. `LOCK TABLE` was a no-op (`CompatNoopStmt`) — pg_locks returned 0 rows for relation locks
2. Views were excluded from `pg_class` (filtered as `Virtual=true`)
3. `CREATE VIEW v(a,b) AS SELECT * FROM t1, t2` produced a 1-column view (counted `*` as one target)
4. Locking a view didn't transitively lock its underlying tables (PostgreSQL does this)
5. Circular view definitions caused stack overflows in the planner

## Changes

### 1. LOCK TABLE Parsing and Execution

**Files:** `internal/parser/ast.go`, `internal/parser/parser.go`, `internal/executor/operators_ddl.go`, `internal/executor/relation_locks.go`

Added `LockTableStmt{Relations []LockTableRelation, Mode string, NoWait bool}` AST node. The parser now properly handles:
- Optional `TABLE` keyword (KwTable)
- Optional `ONLY` keyword (ignored)
- Multiple relation names (`LOCK t1, t2`)
- All 8 PostgreSQL lock modes (`ACCESS SHARE` → `AccessShareLock`, etc.)
- `NOWAIT` keyword (KwNowait)
- Schema-qualified names and search_path resolution

A global `relLockMgr` tracks per-session relation locks in pg_locks. Locks are released in `connTxState.End()` via `ReleaseRelationLocks(c.sess)`, which is called by the server's fast-path COMMIT/ROLLBACK handler.

### 2. Transitive View and Inheritance Locking

**File:** `internal/executor/operators_ddl.go`

`lockRelationTransitively(sess, dbOID, mode, tbl, cat, visited)` mirrors PostgreSQL's behavior:
- For views: walks the view's SELECT body (FROM, FromExprs, JOIN chains, target expressions, WHERE clause) to collect all referenced table/view OIDs, then recursively locks them.
- For tables: locks all inheritance children via `catalog.InheritanceChildren(tbl.OID)`.

This reduces the `lock_view2` query from 1 row to 3 rows (lock_tbl1, lock_tbl1a, lock_view2) as expected.

### 3. pg_class Includes User Views

**File:** `internal/catalog/catalog.go`

Changed the `pg_class.VirtualRows` filter from `if t.Virtual { continue }` to `if t.Virtual && t.View == nil && !t.IsMatView { continue }`. User views now appear with `relkind='v'` so `pg_locks JOIN pg_class ON relation=oid` finds them.

### 4. pg_locks Includes Relation Locks

**File:** `internal/catalog/catalog.go`

Added `RelationLockRowsFunc func() [][]string` alongside `AdvisoryLockRowsFunc`. The pg_locks VirtualRows function now calls both. Wired in `executor/relation_locks.go` init().

### 5. execCreateView Column Count via planSchema

**File:** `internal/executor/operators_ddl.go`

Previously `execCreateView` iterated `s.Query.Targets` which counts `*` as one target even when it expands to multiple columns. Now uses `planSchema` (plan output) when planning succeeds:
- `SELECT * FROM t1, t2` → planSchema has 2 columns → view stored with 2 columns
- Falls back to explicit `s.Columns` aliases when planning fails or returns nil

For OR REPLACE: temporarily drops the existing view before re-planning the new body to prevent the old definition from creating a planning cycle.

Circular-view errors (42P10) are treated as 0A000 (non-fatal) at CREATE VIEW time, matching PostgreSQL which allows creating circular views without validation errors.

### 6. Planner Cycle Guard

**File:** `internal/planner/planner.go`

`viewPlanDepth atomic.Int32` prevents infinite recursion when the planner expands circular view definitions. Depth > 64 returns a `42P10 circular definition` error instead of growing the goroutine stack. Without this guard, `CREATE VIEW lock_view7 AS SELECT * from lock_view2` (where lock_view2 references lock_view3 which references lock_view2) caused a fatal stack overflow.

## Results

| Test | Before | After |
|------|--------|-------|
| lock.sql diffs | 83 | 44 |
| pg_class shows views | no | yes |
| LOCK TABLE in pg_locks | no | yes |
| Transitive view locking | no | yes |
| Circular view crash | crash | safe error |

## Remaining 44 Diffs

- SET ROLE / permission denied (role-based access control not implemented)
- CREATE VIEW WITH (security_invoker) syntax not supported
- C-language function `test_atomic_ops` (language 'c' not supported)
- DROP SCHEMA CASCADE notice message
