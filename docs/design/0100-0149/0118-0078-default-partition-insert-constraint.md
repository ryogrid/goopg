# 0118-0078 — INSERT enforces the DEFAULT partition constraint (M0118-0008 `partition-concurrent-attach` enabler / piece (c))

- **Milestone:** M0118-0008 (DDL / VACUUM / maintenance concurrency)
- **Spec:** `postgres/src/test/isolation/specs/partition-concurrent-attach.spec`
- **Status:** accepted — **enabler, NOT a promotion** (spec stays `defer`). Piece (c) of the `partition-concurrent-attach` interlock.
- **Builds on:** 0118-0075 (ATTACH default-conflict check), 0118-0076 (ATTACH locks the DEFAULT partition), 0118-0077 (ATTACH defers registration until COMMIT).

## Problem — goopg never enforced the default-partition constraint on INSERT

In PostgreSQL every partition carries an implicit *partition constraint* derived
from its `pg_class.relpartbound`. For a **default** partition the constraint is the
negation of every sibling's bounds: a row whose partition-key value is owned by a
non-default sibling does **not** satisfy the default's constraint.
`ExecPartitionCheck` enforces this on **every** insert into the leaf — whether the
row was tuple-routed through the parent or inserted directly into the partition.

goopg had no per-row partition-constraint expression and never performed this
check. Partition *routing* (`routeToPartition`) only ever returns the default when
no non-default sibling matches, so within a single consistent catalog read the
default legitimately owns the routed row — and a **direct** `INSERT` into a default
partition wrote the row unconditionally. This is exactly the behaviour the
`partition-concurrent-attach` spec exercises:

```
step s2i2: insert into tpart_default (i, j) values (110, 'xxx'), … <waiting ...>
step s1c:  commit;
step s2i2: <... completed>
ERROR:  new row for relation "tpart_default" violates partition constraint
```

After 0118-0076/0077, `s2`'s `INSERT INTO tpart_default` blocks on the default
partition's `AccessExclusiveLock` until `s1`'s concurrent `ATTACH PARTITION tpart_2
FOR VALUES FROM (100) TO (200)` commits. By the time the lock is granted, `tpart_2`
is registered in the shared catalog, so `i = 110` now belongs to `tpart_2` and the
default's narrowed constraint rejects the row. goopg instead wrote the row (final
SELECT showed 6 rows, not 3).

## Change — reconstruct and check the default-partition constraint at INSERT time

`insertOp.Next` now calls a new `checkDefaultPartitionInsertConstraint` at the two
points a row is committed to a heap relation:

1. the partition-routed write path, on the routed **leaf** (`partTable`/`partRow`);
2. the non-partitioned write path, on `o.plan.Table` (covers a **direct** INSERT
   into a non-partitioned leaf default partition).

The check walks the target's partition ancestry via `Table.PartitionParentOID`.
For every ancestor that **is** a default partition, it re-routes the row's
corresponding parent partition-key value through the parent's scheme
(`routePartitionKeyToImmediateChild`, one level, no recursion). If that value lands
on a **non-default** sibling, the default's constraint is violated and we raise:

```
23514  new row for relation "<default partition>" violates partition constraint
```

naming the ancestor default at the level where the violation is detected — matching
PG's `ExecPartitionCheckEmitError`. Returning to the spec, the leaf
`tpart_default_default`'s walk finds `tpart_default` (default of `tpart`, routed via
`i`) claims `tpart_2`, so the error names `tpart_default`.

### Why this is the right place and is concurrency-correct

A partition child always physically contains every ancestor partition-key column by
name (partitioning requires the key column to exist in the child), so a
**leaf-ordered** row resolves any ancestor's key directly by column name — no
re-mapping is needed as the walk climbs.

The check reads the **live** catalog at write time, *after* the INSERT's
`RowExclusiveLock` on the default partition has been granted (acquired in
`insertOp.Open`). In `partition-concurrent-attach` that lock blocks behind the open
attach's `AccessExclusiveLock` (0118-0076) until `s1` commits and `tpart_2` is
registered (0118-0077), so the re-route sees `tpart_2` and fails — exactly PG's
"routing decided pre-commit, constraint checked post-commit" sequence.

In ordinary (non-concurrent) operation the check is a no-op: if a non-default
sibling owned the value, routing would have picked that sibling rather than the
default, so the re-route returns the default itself (`sib.OID == cur.OID`). The only
way the re-route disagrees is when a sibling became visible between the routing
decision and the post-lock write — i.e. the concurrent-attach race. The check also
makes a **direct** insert of a sibling-owned value into a default partition fail
even without concurrency, which is correct PG behaviour goopg previously lacked.

### Mechanism (`internal/executor/operators_storage.go`)

- `checkDefaultPartitionInsertConstraint(ctx, leaf, leafCols, leafRow, pos)` — walks
  `PartitionParentOID` (depth-guarded at 8); raises 23514 at the first default
  ancestor whose parent-key re-route lands on a non-default sibling.
- `isDefaultPartitionChild(t)` — true if any `PartitionBound.IsDefault`.
- `routePartitionKeyToImmediateChild(parent, leafCols, leafRow, im, ctx)` — resolves
  `parent.PartitionKey` columns by name from the leaf row, formats them, and calls
  `InMemory.FindRangePartitionForDatums` (RANGE) / `FindPartitionForValue` (LIST) to
  get the **immediate** child. Returns `nil` for expression keys and HASH (out of
  scope → never a false positive).
- `partitionKeyDatumToRangeStr` / `partitionKeyDatumToListStr` — datum→string
  formatting mirroring the corresponding arms of `routeToPartitionDepth`.

Scope is bounded to simple-column RANGE/LIST keys (what every `port` spec uses);
expression-key and HASH defaults conservatively skip the check.

## Effect on the spec (probed, not promoted)

- **Permutation 2** (`s2i2`, direct `INSERT INTO tpart_default`): now matches PG
  byte-for-byte — `<waiting ...>` → `<... completed>` →
  `ERROR: new row for relation "tpart_default" violates partition constraint`, final
  SELECT 3 rows.
- **Permutation 1** (`s2i`, `INSERT INTO tpart` routing *through* the default): the
  constraint check is in place, but `s2i` does not yet **wait** — goopg locks only
  the routed leaf, not the intermediate `tpart_default` along the routing path, so
  the re-route happens before `tpart_2` commits. Needs routing-path
  `RowExclusiveLock`s (next piece).
- **Permutation 3** (reverse): the attach must wait for `s2`'s prior committed
  INSERT and re-scan the default leaf (`checkDefaultPartitionDataConflict` already
  produces the 23P01 message); needs the same routing-path lock so the attach's
  default `AccessExclusiveLock` contends with `s2`'s routed insert.

The remaining two permutations are the routing-path-lock piece, milestone-shared
with `alter-table-4`.

## Verification

- `go test ./internal/executor/` (full package) — PASS, incl. new
  `default_partition_constraint_test.go`:
  - `TestDefaultPartitionInsertConstraint` — direct insert of a sibling-owned value
    into a default → 23514; routed insert of a default-owned value → OK; after
    `DETACH` of the owning sibling the same value inserts.
  - `TestDefaultPartitionConstraintSubPartitioned` — multi-level walk-up: violation
    two levels above the routed leaf names the intermediate default.
- No regression: `TestPort_IsolationDetachPartitionConcurrently1`,
  `TestPort_IsolationPartitionDropIndexLocking`, `TestPort_IsolationInheritTemp`
  (strict) PASS.
- Probe: `partition-concurrent-attach` permutation 2 now matches PG; spec stays
  `defer` pending the routing-path locks for permutations 1 and 3.
- `go build ./...` clean; pgbench smoke = pre-commit hook.

## Oracle

Mirrors PostgreSQL's `ExecPartitionCheck` / `ExecPartitionCheckEmitError`
(`src/backend/executor/execMain.c`) and the default-partition constraint built by
`get_qual_for_list` / `get_qual_for_range` (`src/backend/partitioning/partbounds.c`):
the default partition's constraint is the negation of all sibling bounds, enforced
on every tuple inserted into the leaf. goopg, lacking a materialised constraint
expression, reconstructs the equivalent test by re-routing the parent key through
the live partition descriptor.
