# 0118-0073 — pg_stat_activity retains the last query on idle (partition-drop-index-locking blocker 3)

**Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency)
**Spec:** `partition-drop-index-locking` — enabler, NOT a promotion.
**Status:** landed. Closes **blocker 3 of 4** (see 0118-0071 for the blocker map).

## Problem

The `partition-drop-index-locking` spec's `s3getlocks` step joins `pg_locks` to
`pg_stat_activity` and selects `s.query`:

```sql
SELECT s.query, c.relname, l.mode, l.granted
FROM pg_locks l
     JOIN pg_class c ON l.relation = c.oid
     JOIN pg_stat_activity s ON l.pid = s.pid
WHERE c.relname LIKE 'part_drop_index_locking%'
ORDER BY s.query, c.relname, l.mode, l.granted;
```

At the first snapshot, session `s1` has run `s1select` and is sitting
idle-in-transaction holding `AccessShare` locks; session `s2` is blocked
(active) inside `DROP INDEX …`. PostgreSQL's expected output therefore shows
the verbatim last query for **both** backends, each terminated with `;`:

```
DROP INDEX part_drop_index_locking_idx;             |…
SELECT * FROM part_drop_index_locking_subpart_child;|…
```

goopg diverged two ways:

1. **Empty query for the idle backend.** `s1`'s `query` column was blank.
   PostgreSQL's `pg_stat_activity.query` "shows the last query that was
   executed" in every non-`active` state and only NULLs out a backend that has
   never run a statement. goopg cleared the stored query on return to idle.

2. **Missing trailing `;`.** Even the active `DROP INDEX` row rendered as
   `DROP INDEX part_drop_index_locking_idx` (no `;`). PostgreSQL's
   isolationtester sends each step's body verbatim — including the terminating
   `;` — as a single simple-query, and `pg_stat_activity.query` echoes it byte
   for byte.

## Change

Two surgical fixes — one engine, one test-runner fidelity.

### 1. Engine: retain the last query on idle

`internal/activity/registry.go`, `ActivityRegistry.UpdateState`: removed the
`else if state == "idle" { c.Query.Store(nil) }` branch. The dispatcher calls
`UpdateState(procNum, "active", q)` before executing a statement and
`UpdateState(procNum, "idle", "")` after; with the clear gone, the active
query text persists through the idle transition, matching PostgreSQL's
"last query executed" semantics. `QueryStart` is untouched on idle (it already
only advances when a non-empty query arrives), and a never-run backend still
reports NULL (`Query` starts `nil`). Regression: `TestUpdateStateRetainsQueryOnIdle`.

### 2. Runner fidelity: preserve the trailing `;`

`internal/testport/framework/isolation_runner.go`, `execStep`: goopg's
isolation runner splits a step body into individual statements via
`splitSQLStatements` (which strips `;` terminators) because lib/pq executes one
statement per `QueryContext`. For a **single-statement** step the split result
dropped the `;` that PostgreSQL would have sent and stored. `execStep` now
sends the trimmed verbatim step body (`SELECT … ;`) when there is exactly one
statement, so the query goopg records matches PostgreSQL. Multi-statement steps
keep the split form. `splitSQLStatements` and its unit tests are unchanged.

## Why two halves

`pg_stat_activity.query` reflects *what the client sent*; the engine stores it
verbatim. So both ends must agree — the engine has to keep the text past idle,
and the runner has to send the text PostgreSQL would have sent. Either fix alone
leaves the `s.query` column wrong.

## State column is intentionally out of scope

`s3getlocks` selects only `s.query` (not `s.state`), so this spec is unaffected
by goopg reporting `idle` where PostgreSQL reports `idle in transaction`
(`ActivityRegistry.BeginTransaction` is not wired into the simple-query
dispatch path). That divergence is left for the transactional-DDL /
cross-session catalog work and does not block this spec.

## Verification

- `go build ./...` clean.
- `go test ./internal/activity/` + `-race`: PASS (new `TestUpdateStateRetainsQueryOnIdle`).
- `go test ./internal/testport/framework/`: PASS (`splitSQLStatements` table unchanged).
- Throwaway probe of `partition-drop-index-locking.spec`: the entire first
  `s3getlocks` snapshot now matches PG byte-for-byte (both the idle `SELECT …;`
  row and the active `DROP INDEX …;` rows, with `;`). The remaining diff is
  **blocker 4** only — the second snapshot shows 5 vs 6 rows because goopg drops
  the index's `pg_class` row synchronously at `DROP INDEX` instead of keeping it
  visible until commit (transactional-DDL cross-session catalog MVCC, milestone-sized).
- No-regression batch (strict, pass-required): `lock-committed-update`,
  `drop-index-concurrently-1`, `create-trigger`, `inherit-temp`,
  `truncate-conflict`, `reindex-concurrently`, `multiple-cic` — all PASS.

## Remaining blockers (partition-drop-index-locking stays `defer`)

4. **Transactional-DDL cross-session catalog visibility** (milestone-sized,
   shared with `alter-table-4` / partition-concurrent-attach): the second
   `s3getlocks` must still show the dropped index's `pg_class` row and locks
   until `s2commit`. goopg removes the index from the shared in-memory catalog
   synchronously, so the join loses the row immediately (5 vs 6 rows).

See 0118-0071 (DROP INDEX relation-tree locking) for the full blocker map and
0118-0072 (SELECT locks the scanned relation's indexes) for blocker 2.
