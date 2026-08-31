# 0100-0006b — UPSERT DO UPDATE: HOT-equivalent index-key reuse

**Status:** accepted (final fix of M0100-0006b — perm 5 full pass)
**Milestone:** M0100 — RC Isolation Suite runtime correctness & spec pass
**Spec:** `postgres/src/test/isolation/specs/insert-conflict-specconflict.spec` (perm 5)

## Problem

After parts (a)/(b)/(c) landed, perm 5 of `insert-conflict-specconflict`
diverged from the PG oracle by exactly two NOTICE lines. After `s2_commit`, the
expected output shows s1's `ON CONFLICT DO UPDATE` re-evaluating only the
**arbiter** expression once:

```
step s2_commit: COMMIT;
s1: NOTICE:  blurt_and_lock_123() called for k1 in session 1
s1: NOTICE:  acquiring advisory lock on 2
step s1_upsert: <... completed>
```

goopg additionally re-evaluated the **non-unique** index expression
`upserttest_key_idx ON upserttest((blurt_and_lock_4(key)))`:

```
s1: NOTICE:  blurt_and_lock_123() called for k1 in session 1
s1: NOTICE:  acquiring advisory lock on 2
s1: NOTICE:  blurt_and_lock_4() called for k1 in session 1   <- EXTRA
s1: NOTICE:  acquiring advisory lock on 4                     <- EXTRA
step s1_upsert: <... completed>
```

### Root cause

PostgreSQL's `ON CONFLICT DO UPDATE` performs a heap update. The `SET data =
upserttest.data || …` clause changes only `data`; no indexed column changes, so
the update is **HOT** (`heap_update` → `HeapTupleIsHeapOnly`) and inserts **zero**
index tuples — `ExecInsertIndexTuples` is skipped entirely. The arbiter
expression `blurt_and_lock_123` is still evaluated once, but as
`ExecBuildArbiterKey` during the speculative-insert conflict re-probe, **not** as
index maintenance. The non-unique index expression is never re-evaluated.

goopg has no HOT update: `applyUpdate` writes a fresh heap tuple at `newPtr` and
re-inserts an entry into **every** index so subsequent index scans find the new
version. `maintainUniqueIndexesForInsertSkipArbiter` iterates *all* indexes
(its name predates non-unique coverage) and, for the expression index, calls
`encodeExprIndexKey`, which evaluates `blurt_and_lock_4(key)` → the two extra
NOTICEs.

## Fix

Mirror PG's "no indexed column changed → no expression re-evaluation" without
implementing full HOT chains, by **caching the index keys at speculative-insert
time and reusing them on the DO UPDATE retry** when the indexed columns are
provably unchanged.

`internal/executor/operators_upsert.go`:

- `upsertOp` gains `specIndexKeys map[uint32][]byte` (index OID → key bytes) and
  `specInsertedLeaf Row`, both reset per source row and repopulated by
  `applyInsert`.
- `maintainNonArbiterIndexesCapture` replaces the call to
  `maintainUniqueIndexesForInsertSkipArbiter` in `applyInsert`: it inserts every
  non-arbiter index entry **and** returns the keys it computed. This is the one
  legitimate evaluation of `blurt_and_lock_4` (matches the PG oracle's single
  speculative-insert evaluation).
- `maintainNonArbiterIndexesForUpdate` replaces the maintenance calls in
  `applyUpdate`. For each non-arbiter index, if a cached key exists **and**
  `indexKeyUnchangedFromSpec` proves the index's referenced base columns are
  identical between the speculatively-inserted row and the updated row, it
  inserts the **cached** key → `newPtr` (no re-evaluation). Otherwise it falls
  back to evaluating the key (prior behaviour).
- `indexKeyUnchangedFromSpec` resolves each index's referenced base columns
  (plain `idx.Columns` entries directly; expression columns via
  `collectExprColumnNames` over `idx.ColExprs`) and compares the corresponding
  `Datum`s with `datumEquals`.
- `collectExprColumnNames` is a conservative AST walker: it returns `false` on
  any node shape it does not recognise (subquery, param, …), forcing the safe
  fallback (re-evaluate). It handles the shapes that can legally appear in an
  index expression (column refs, function calls, operators, casts, CASE, ROW,
  array constructors/subscripts, IS NULL, literals).

The orphaned `maintainUniqueIndexesForInsertSkipArbiter` is removed.

### Why this is correct, not just NOTICE-matching

The reused cached key is **byte-identical** to what the fallback would compute:
`indexKeyUnchangedFromSpec` only reuses when every base column the index reads
is equal between the two rows, so the expression would produce the same value.
The resulting btree state (`cached_key → newPtr`) is exactly what the previous
code produced — the change elides only the redundant side-effectful evaluation.
When an indexed column *does* change (e.g. `DO UPDATE SET key = …`, or a direct
conflict that never speculatively inserted), the cache miss / change check forces
a real evaluation, so index correctness is preserved.

## Scope / known parallel

The plain `UPDATE` path (`updateOp`, `operators_storage.go`) still re-evaluates
expression indexes on every row via `maintainUniqueIndexesForInsert`; it already
maintains a `t_ctid` forward chain for EPQ followers. Bringing it to true HOT
(skip index maintenance when no indexed column changed) is a larger, separate
change and is not required by any current ported test. Recorded here so the
asymmetry is intentional and discoverable.

## Verification

- `go test -run TestPort_IsolationInsertConflictSpecconflict ./internal/testport/`
  → PASS (all 5 permutations).
- `go test ./internal/executor/` → ok.
- `go test -run 'TestPort_IsolationInsertConflict|TestPort_IsolationMerge'
  ./internal/testport/` → ok (no row-count regression on the upsert/merge paths).
- `scripts/tpch-spotcheck.sh` → SKIPPED (no data dir); the change touches only
  ON CONFLICT index maintenance, which TPC-H does not exercise.
