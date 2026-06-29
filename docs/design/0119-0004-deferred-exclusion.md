# 0119-0004 — Deferred EXCLUDE constraint checking (M0119-0004)

Status: accepted

## Problem

PostgreSQL lets an `EXCLUDE` constraint be declared `DEFERRABLE INITIALLY
DEFERRED` (or deferred at runtime with `SET CONSTRAINTS … DEFERRED`). A deferred
exclusion check tolerates a **transient** conflict while the transaction runs and
enforces the constraint only at COMMIT (or at `SET CONSTRAINTS … IMMEDIATE`). The
canonical case:

```sql
CREATE TABLE t (c int, EXCLUDE (c WITH =) DEFERRABLE INITIALLY DEFERRED);
BEGIN;
INSERT INTO t VALUES (1);
INSERT INTO t VALUES (1);   -- transient conflict, allowed (deferred)
DELETE FROM t WHERE ctid = (SELECT min(ctid) FROM t);
COMMIT;                     -- OK: only one row with c=1 survives
```

goopg already had the parallel machinery for **foreign keys**
([[0119-0004-set-constraints-deferred]], [[0119-0004-deferred-ri-fresh-snapshot]])
and **UNIQUE/PK** ([[0119-0004-deferred-unique]], [[0119-0004-deferred-unique-nnd]]):
a per-session queue filled at DML time and drained at COMMIT under a fresh
snapshot. It had **no** deferred-exclusion queue. An `EXCLUDE … DEFERRABLE
INITIALLY DEFERRED` parsed and round-tripped through the catalog (the backing
`Index.Deferrable` / `Index.InitiallyDeferred` flags — set in `operators_ddl.go`
for both the `WITH =` btree and `WITH &&` gist forms) but was enforced
**immediately** at the INSERT, so a legitimate transient conflict wrongly raised
23P01. This was one of the three documented follow-ups under M0119-0004.

## PostgreSQL semantics (mirrored)

Exclusion constraints are enforced by `check_exclusion_or_unique_constraint`
(`postgres/src/backend/executor/execIndexing.c`) which, for a deferred index,
does a partial check at insert time and queues a recheck via the deferred-trigger
machinery (`trigger.c`). At the deferral boundary the recheck re-probes the
candidate's key and fails if any *other* live tuple conflicts with it.

- Only `DEFERRABLE` constraints are affected; a `NOT DEFERRABLE` EXCLUDE keeps its
  immediate check. `SET CONSTRAINTS` never makes a `NOT DEFERRABLE` constraint
  deferred.
- The deferral state is constraint-kind-agnostic — PG's `AfterTriggerSetState`
  applies the same per-transaction override to FK, UNIQUE and EXCLUDE
  constraints, so goopg's `BasicSession.constraintDeferredByName` is shared by all
  three (`ExclusionConstraintDeferred` is a thin wrapper, like
  `UniqueConstraintDeferred`).
- NULL key columns never conflict (exclusion ignores NULLs, like UNIQUE).
- The error is `23P01 exclusion_violation`:
  `conflicting key value violates exclusion constraint "<name>"`, DETAIL
  `Key (c)=(v) conflicts with existing key (c)=(v).`. Captured byte-for-byte from
  PG 18.3 (`./postgres/local_install`).

## Design

A direct mirror of [[0119-0004-deferred-unique]]:

### Enqueue (DML time)

`checkExclusionConstraintsForInsert` (`operators_storage.go`) is the single
INSERT-time enforcement chokepoint (exclusion is not re-checked on UPDATE in
goopg today, matching the absence of an UPDATE-side exclusion path). Before the
immediate `switch idx.ExclusionOp`, it now consults
`excludeCheckDeferred(ctx, idx)` and, when true, calls
`queueDeferredExclusionCheck` and `continue`s instead of raising:

- `excludeCheckDeferred` short-circuits on `!idx.IsExclusion || !idx.Deferrable`,
  then defers to `BasicSession.ExclusionConstraintDeferred(name, initiallyDeferred)`
  — exactly `idx.Deferrable && InitiallyDeferred && InExplicitTransaction()` with
  no `SET CONSTRAINTS` in effect.
- `queueDeferredExclusionCheck` captures the candidate's exclusion key without
  holding live catalog/Row pointers across statements:
  - `WITH =` (btree): the encoded btree key via `encodeIndexKeyFromCols` (NULL key
    → skip), plus the pre-rendered DETAIL (`buildExclusionConstraintDetail`, whose
    candidate-value-for-both-sides form already matches PG for an equality
    conflict).
  - `WITH &&` (gist overlap): the candidate box text (NULL box → skip). The DETAIL
    is built at recheck time, where the *conflicting* box is known.

### Session state (`session.go`)

- New `deferredExclChecks []DeferredExclusionCheck` queue (reset in
  `EndExplicitTransaction`, like every other per-txn deferral state — PG resets
  the deferral state at every txn boundary).
- `DeferredExclusionCheck{TableName, IndexName, ExclusionOp, Key, BoxStr, Detail}`.
  `IndexName == ` the constraint name (the `SET CONSTRAINTS` match key).
- `AddDeferredExclusionCheck` (dedup on `IndexName`+`Key`+`BoxStr`),
  `TakeDeferredExclusionChecks`, `TakeDeferredExclusionChecksMatching(all, names)`.

### Drain (COMMIT + SET CONSTRAINTS IMMEDIATE)

`RunDeferredExclusionChecks` / `runAllDeferredExclusionChecks`
(`deferred_exclusion.go`) drain the queue under a freshly pushed "latest"
snapshot (`mvcc.Manager.FreshSnapshot()`, identical to deferred RI / UNIQUE) so
the COMMIT-time check reflects the final committed state; own uncommitted writes
stay visible via `isLiveForUniqueCheck`'s self classification. Wired at all three
deferral boundaries, after the FK and UNIQUE drains:

- `transactionOp.execCommit` (`operators_tx.go`) — executor commit path.
- the simple-query COMMIT in `server/dispatch.go` — bypasses `execCommit`; the
  `ExecError.Code` (`23P01`) drives the wire SQLSTATE in the shared rollback block.
- `setConstraintsOp.Next` (`operators_tx.go`) for `SET CONSTRAINTS … IMMEDIATE`,
  via `TakeDeferredExclusionChecksMatching`.

### Recheck (the ≥2 rule)

At COMMIT the candidate row is already present, so a violation is "two or more
live visible tuples conflict", not "any other tuple conflicts" — the candidate is
itself one match. A transient conflict resolved before COMMIT (one collider
deleted) leaves a single live match and passes.

- `recheckDeferredExclusionEq` — `tree.RangeScan(key, key, …)` counting distinct
  live `ItemPointer`s; `≥2` → 23P01. A clone of `recheckDeferredUniqueKey` raising
  `exclusion_violation` instead of `unique_violation`.
- `recheckDeferredExclusionOverlap` — seq-scans the heap (mirroring
  `checkGistOverlapExclusion`) counting live tuples whose box overlaps the
  candidate's box. A box always overlaps itself, so `≥2` → 23P01. The DETAIL
  reports a *differing* overlapping box when present, else the candidate's own
  (equal-box conflict, which PG also renders with identical values).

## Blast radius

Bounded behind `idx.IsExclusion && idx.Deferrable`. Every non-deferrable
exclusion index keeps the existing immediate path byte-for-byte; pgbench/TPC-H
carry no EXCLUDE constraints. No parser/catalog change (the deferrable flags were
already populated for exclusion indexes — dump fidelity is now load-bearing).

## Testing

- `internal/testport/deferred_exclusion_e2e_test.go`:
  - `TestPort_InitiallyDeferredExclusionCommit` — transient conflict resolved →
    COMMIT OK (1 row); conflict surviving to COMMIT → 23P01 with the PG DETAIL,
    rollback (0 rows); a plain NOT DEFERRABLE EXCLUDE still raises immediately at
    the 2nd INSERT.
  - `TestPort_SetConstraintsExclusionDeferral` — `DEFERRABLE INITIALLY IMMEDIATE`
    + `SET CONSTRAINTS ALL DEFERRED` tolerates the transient conflict, then
    `SET CONSTRAINTS ALL IMMEDIATE` raises 23P01 right there.
- All goldens captured from PostgreSQL 18.3 (`./postgres/local_install`).
- Regression: full `internal/executor` package + `-race` over
  `Exclusion|DeferredUnique|Upsert|Conflict|NullsNotDistinct|SetConstraints`;
  prior deferred FK/UNIQUE/NND e2e + `fk-snapshot` green; pgbench smoke.

## Follow-ups

The two remaining M0119-0004 items are unrelated to deferred constraints: the
pg_dump 002–010 catalog-view parity battery (DU-002) and extended-protocol
commit-time deferral (architecturally entangled — goopg's extended protocol is
auto-commit-per-statement). With this loop **all four deferrable constraint kinds
— FK, UNIQUE/PK, UNIQUE NULLS NOT DISTINCT, and EXCLUDE — honour
`DEFERRABLE INITIALLY DEFERRED` and `SET CONSTRAINTS` at COMMIT.**
