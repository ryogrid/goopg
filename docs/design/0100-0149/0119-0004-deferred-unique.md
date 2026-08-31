# 0119-0004 — Deferred UNIQUE / PRIMARY KEY constraint checking (M0119-0004)

Status: accepted

## Problem

PostgreSQL lets a `UNIQUE` or `PRIMARY KEY` constraint be declared
`DEFERRABLE INITIALLY DEFERRED` (or deferred at runtime with
`SET CONSTRAINTS … DEFERRED`). A deferred uniqueness check tolerates a
**transient** duplicate while the transaction runs and enforces uniqueness only
at COMMIT (or at `SET CONSTRAINTS … IMMEDIATE`). The canonical case is

```sql
UPDATE t SET id = id + 1;   -- contiguous key range
```

where intermediate row states momentarily collide but the final state is
distinct.

goopg already had the parallel machinery for **foreign keys**
([[0119-0004-set-constraints-deferred]], [[0119-0004-deferred-ri-fresh-snapshot]]):
a per-session queue filled at DML time and drained at COMMIT under a fresh
snapshot. It had **no** deferred-uniqueness queue — `DEFERRABLE INITIALLY
DEFERRED` on a UNIQUE/PK constraint parsed and round-tripped through pg_dump
(catalog flags `Index.Deferrable` / `Index.InitiallyDeferred`, DU-002 slice 139)
but was enforced **immediately** at the INSERT/UPDATE, so a legitimate transient
duplicate wrongly raised 23505. This was the documented follow-up at the end of
the `SET CONSTRAINTS` design.

## PostgreSQL semantics (mirrored)

`postgres/src/backend/access/nbtree/nbtinsert.c` (`_bt_check_unique` /
`index_insert` with `UNIQUE_CHECK_PARTIAL`) and the deferred-trigger machinery in
`postgres/src/backend/commands/trigger.c`:

- A deferred unique index does a *partial* check at insert time (it never
  blocks) and queues a recheck event. At the deferral boundary the recheck
  re-finds the key and fails if more than one live tuple carries it.
- Only `DEFERRABLE` constraints are affected; a `NOT DEFERRABLE` UNIQUE/PK keeps
  its immediate check. `SET CONSTRAINTS` never makes a `NOT DEFERRABLE`
  constraint deferrable.
- `… IMMEDIATE` runs the queued rechecks right away — a violation raises at that
  statement, not at COMMIT.

## Design

The implementation mirrors the deferred-FK structure one-for-one.

### Catalog (no change)

`catalog.Index.Deferrable` / `.InitiallyDeferred` were already populated by the
DDL path for UNIQUE/PK constraints. No parser or catalog work was needed.

### Session queue (`internal/executor/session.go`)

`BasicSession` gains `deferredUniqChecks []DeferredUniqueCheck`, reset by
`EndExplicitTransaction` alongside `deferredFKChecks`.

```go
type DeferredUniqueCheck struct {
    TableName string
    IndexName string // == the UNIQUE/PK constraint name (SET CONSTRAINTS match key)
    Key       []byte // the candidate btree key whose uniqueness must hold at COMMIT
    Detail    string // pre-rendered "Key (...)=(...) already exists." for the 23505
}
```

New methods `AddDeferredUniqueCheck` (dedup on `(IndexName, Key)`),
`TakeDeferredUniqueChecks`, and `TakeDeferredUniqueChecksMatching(all, names)`
(the `… IMMEDIATE` subset), mirroring the FK ones.

The deferral decision is constraint-kind-agnostic in PG, so the existing
per-name / ALL / declared-default resolver was extracted to
`constraintDeferredByName`; `FKConstraintDeferred` and the new
`UniqueConstraintDeferred` both delegate to it.

### Enqueue (`internal/executor/operators_storage.go`)

`checkUniqueIndexesForInsert` and `checkUniqueIndexesForUpdate` gain, right
before the immediate `uniqueCheckWithWait` probe:

```go
if uniqueCheckDeferred(ctx, idx) {
    queueDeferredUniqueCheck(ctx, tbl, idx, cols, row, key)
    continue
}
```

`uniqueCheckDeferred` (in the new `internal/executor/deferred_unique.go`) is the
exact analogue of `fkCheckDeferred`: a constraint can only be deferred when it is
`DEFERRABLE`, inside an explicit transaction, and the session's effective
deferral (per-name override → ALL mode → declared `INITIALLY DEFERRED`) says so.
For a plain `NOT DEFERRABLE` index the very first `!idx.Deferrable` test
short-circuits to `false`, so **every non-deferrable UNIQUE/PK keeps its exact
prior immediate path** — zero blast radius for pgbench/TPC-H (PK is not
deferrable).

### Commit-time recheck (`internal/executor/deferred_unique.go`)

`RunDeferredUniqueChecks(ctx, sess)` drains the queue and, like
`runAllDeferredFKChecks`, installs a fresh `mvcc.Manager.FreshSnapshot()` for the
duration so a key inserted by a transaction that committed after this
transaction's snapshot is visible. For each queued key it runs
`recheckDeferredUniqueKey`:

> `RangeScan(key, key)` the backing btree and count the **distinct live visible**
> heap tuples (deduped by `ItemPointer`, liveness via the existing
> `isLiveForUniqueCheck`). **Two or more** is a violation — the candidate row is
> itself one of them, so the deferred predicate is "≥2 live", versus the
> immediate predicate's "any other live". A transient duplicate resolved before
> COMMIT (the `id+1` swap; the superseded version carries `xmax = self` and is
> dead) leaves one live tuple per key and passes.

A violation returns a `23505` `*ExecError` (`Pos: 0`, as PG raises at the
deferral boundary).

### Commit chokepoints

Both commit paths already funnel deferred FK checks; the unique drain is added
immediately after:

- `transactionOp.execCommit` (`operators_tx.go`) — after the FK block, before
  `TxnMgr.Commit`; a violation rolls back and returns the error.
- The simple-query dispatcher (`internal/server/dispatch.go`, `case
  planner.TxCommit`) — which bypasses `execCommit`. The FK and UNIQUE drains
  share one rollback block: `deferErr := RunDeferredFKChecks(...)`; if nil,
  `deferErr = RunDeferredUniqueChecks(...)`. The `*ExecError`'s own `Code` drives
  the wire SQLSTATE, so the same block emits 23503 for FK and 23505 for UNIQUE.

### `SET CONSTRAINTS … IMMEDIATE`

`setConstraintsOp` (`operators_tx.go`) already drains the matching FK subset; it
now also takes the matching unique subset (`TakeDeferredUniqueChecksMatching`)
and runs `runAllDeferredUniqueChecks`, so a pending deferred duplicate raises at
the `SET CONSTRAINTS … IMMEDIATE` statement.

## Blast radius

Gated entirely on `idx.Deferrable`, which is false for every non-deferrable
UNIQUE/PK (all of pgbench/TPC-H). With no `DEFERRABLE` constraint and no
`SET CONSTRAINTS`, not a single new branch beyond the cheap `!idx.Deferrable`
test executes; the immediate `uniqueCheckWithWait` path is byte-for-byte
unchanged. The new session field is zero-valued and reset at every transaction
boundary.

## Limitations / follow-ups

- **NULLS NOT DISTINCT + DEFERRABLE INITIALLY DEFERRED**: the enqueue path keys
  off the encoded btree key, which is `nil` for a NULL key column, so a deferred
  *NND* duplicate of NULL patterns is not yet queued (it still uses the immediate
  NND heap-scan path). Rare combination; recorded as a follow-up.
- **EXCLUDE constraints**: `checkExclusionConstraintsForInsert` is not yet
  deferral-aware (no deferred-exclusion queue). Tracked alongside this.
- **Extended-protocol deferral** of `SET CONSTRAINTS` (the executor session is
  not threaded into the extended utility fast path) — shared limitation with the
  FK slice.

## Tests

- `internal/testport/deferred_unique_e2e_test.go`:
  - `TestPort_InitiallyDeferredUniqueCommit` — plain `INITIALLY DEFERRED`, no
    override: the `UPDATE … SET val = val + 1` transient-duplicate swap commits;
    two rows sharing `val=99` raise 23505 **at COMMIT** and roll back.
  - `TestPort_SetConstraintsUniqueDeferral` — `INITIALLY IMMEDIATE` UNIQUE:
    immediate-fail control, `SET CONSTRAINTS ALL DEFERRED` → fail at COMMIT,
    `SET CONSTRAINTS ALL IMMEDIATE` → fail at the SET, and a clean deferred
    insert commits.
- Regression: full `internal/executor` package, `-race` on
  executor+mvcc unique/upsert/conflict/constraint subsets, and the existing FK
  deferral e2e + `TestPort_IsolationFk*` group all pass.
