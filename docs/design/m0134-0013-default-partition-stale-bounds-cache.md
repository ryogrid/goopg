# M0134-0013 — default-partition validation reads a stale denormalized bounds cache

Status: implemented (2026-08-20)
Milestone: M0134 (regress-sql `failed`/`not-tried` digestion) — case `insert.sql`
Related: `docs/design/m0134-0012-list-partition-numeric-routing.md` (same
"sibling formatter/validator drifted" pattern, previous loop)

## 1. Symptom

`scripts/pg-regress-runner.sh --verbose insert` diverges from PG 18.3 by 1062
diff lines. One root cause explains ~170 of them:

```sql
create table part_default partition of list_parted default;   -- insert.sql:239
drop table part_default;                                      -- insert.sql:248
create table part_default partition of list_parted default    -- insert.sql:252
    partition by range (b);
```

The third statement is a no-output DDL in PG. goopg raises:

```
ERROR:  partition "part_default" conflicts with existing default partition "part_default"
```

Because `part_default` is then never created, `insert.sql:253-266` cascade into
`relation "part_default" does not exist`, and the closing
`select tableoid::regclass, * from list_parted` row set diverges wholesale. The
same shape recurs at `insert.sql:471` (`mlparted_defd`). None of that downstream
noise is an independent bug — it is all confounded by this one error.

Net effect outside the test suite: **once a DEFAULT partition has been dropped,
its parent can never accept another DEFAULT partition for the lifetime of the
catalog entry.**

## 2. Root cause

goopg keeps *two* independent records of a partitioned table's children:

1. `catalog.InMemory.PartitionChildren(parentOID)`
   (`internal/catalog/catalog.go:4520`) — re-resolves each cached child OID
   against the live `ns.tables` map on **every call**. `catalog.DropTable`
   (`catalog.go:20262`) does a synchronous `delete(ns.tables, k)`, so a dropped
   child simply fails to resolve and drops out of the result. This source is
   **self-healing**.
2. `parent.PartitionBounds` — a denormalized aggregate appended to at
   `internal/executor/operators_ddl.go:5011` on every successful
   `CREATE TABLE ... PARTITION OF`, and **never pruned by any removal path**.

The live validator `validateDefaultPartition`
(`internal/executor/operators_ddl.go:5402`, reached from `validatePartitionChild`
at `:5393`, itself called at `:4842`) scans source (2) — the stale cache — so a
dropped DEFAULT partition's bound entry keeps rejecting new defaults forever.

### The sibling-pair inversion (carry-forward lesson from M0134-0012)

A second, structurally correct validator already exists:
`validateDefaultPartitionConflict`
(`internal/executor/operators_ddl_partition.go:854`) walks
`im.PartitionChildren(parent.OID)` — source (1) — and is therefore immune. It is
**dead code**: its only caller, `validatePartitionChildBounds`
(`operators_ddl_partition.go:344`), has zero references anywhere in the tree.

This is the M0134-0012 lesson repeating with the same polarity: the copy that
*looks* canonical (dedicated `operators_ddl_partition.go` file, uses catalog
helpers) is the unreachable one, while the plain inline function buried in the
40k-line `operators_ddl.go` is both live and wrong. When a duplicated
validator/formatter pair is found, determine which copy is stale by call-graph,
not by appearance.

## 3. PostgreSQL oracle

`src/backend/partitioning/partbounds.c:2895-2923`, `check_new_partition_bound()`,
`spec->is_default` arm:

```c
if (spec->is_default)
{
    if (boundinfo == NULL || !partition_bound_has_default(boundinfo))
        return;
    ereport(ERROR, (errcode(ERRCODE_INVALID_OBJECT_DEFINITION),
             errmsg("partition \"%s\" conflicts with existing default partition \"%s\"", ...)));
}
```

`boundinfo` derives from `partdesc = RelationGetPartitionDesc(parent, false)`
(`partbounds.c:2900`), rebuilt from a live `pg_inherits`/`pg_class` scan by
`RelationBuildPartitionDesc` (`src/backend/utils/cache/partcache.c`) whenever the
parent's relcache entry is invalidated. **PG holds no cached aggregate analogous
to `parent.PartitionBounds`** — it recomputes the partition descriptor from the
source of truth on every check. That is the structural reason this class of bug
cannot occur upstream, and it is why the fix is "read the recomputed source",
not "add another cache-invalidation site".

## 4. Decision

**Filter the parent-side bound list by the set of still-live children.**
`validateDefaultPartition` keeps scanning `parent.PartitionBounds` — that list is
correct in *content*, just never pruned — but skips any entry whose `ChildName` no
longer appears in `im.PartitionChildren(parent.OID)`. This reproduces PG's
recompute-from-truth semantics (a dropped child is simply absent from the rebuilt
partition descriptor) with a ~15-line change to one function and no signature
change.

### Correction: the first attempt checked the wrong tree level

The initial implementation scanned each live *child's* own `PartitionBounds` for
`IsDefault`. That is wrong, and the gate caught it. `Table.PartitionBounds` is the
**parent-side** list of a table's children's bounds — each entry carries a
`ChildName` field naming the child that owns it (`catalog.go:1563`), and the
writer at `operators_ddl.go:5011` appends to the *parent* on each child creation.
So a child's `PartitionBounds` describes that child's own sub-partitioning, i.e.
the parent's *grandchildren*.

In `insert.sql`, `part_xx_yy` is an ordinary (non-default) child of `list_parted`
that is itself partitioned and owns a default child `part_xx_yy_defpart`. The
child-level scan matched that grandchild's `IsDefault` and reported `part_xx_yy`
as `list_parted`'s existing default:

```
ERROR:  partition "part_default" conflicts with existing default partition "part_xx_yy"
```

— a *different* wrong answer, at the same 1061-line diff. The isolated unit tests
passed only because their fixtures had no sub-partitioned sibling.

**Consequently the dead sibling `validateDefaultPartitionConflict`
(`operators_ddl_partition.go:854`) is NOT a correct implementation** — it has
exactly this wrong-level bug. §2's framing of it as "the correct twin" was wrong
and is retained above only as the trail of the mistake. The genuine lesson is
narrower than the M0134-0012 one and worth more: **an unreachable sibling has
never been executed, so it carries no evidence of correctness in either
direction.** Dead code is not a reference implementation; verify it against the
oracle before mirroring it, exactly as you would new code.

### Alternative rejected: prune `parent.PartitionBounds` on removal

Teaching the removal paths to prune the parent-side cache requires fixing
*multiple* sites for one denormalized field — `dropTableByRefImmediate`
(`operators_ddl.go:6849`), which has no parent back-reference today, **plus**
`ALTER TABLE ... DETACH PARTITION`, which clears only the *child*'s bounds
(`operators_ddl.go:9240`, `:9287`) and never touches the parent's. That is
strictly more surface for no additional correctness, and it preserves a cache PG
does not have. Rejected.

### Blast radius

`parent.PartitionBounds` used as an aggregate list has exactly two sites in the
whole tree: the writer at `operators_ddl.go:5011` and the reader at `:5403` (the
buggy validator). The chosen fix keeps both — it only adds a liveness filter to
the reader — so no other code path changes behavior. The field stays as it is:
removing the now-suspect cache entirely is a separate cleanup, ledgered.

`catalog.VisiblePartitionChildren` (`catalog.go:4718`) is **not** involved: it
filters on `DetachPendingEpoch` for `DETACH PARTITION CONCURRENTLY` visibility
only. Applying it here would have been a no-op — plain `DROP TABLE` is not a
detach.

## 5. Verification

FAIL-pre reproducer (fails today with 42P16, must succeed after):

```sql
create table t_dp (a text) partition by list (a);
create table t_dp_def partition of t_dp default;
drop table t_dp_def;
create table t_dp_def2 partition of t_dp default;   -- 42P16 pre-fix
insert into t_dp values ('x');                      -- must route into t_dp_def2
```

Guard tests live in `internal/executor/`. Two guards are mandatory, both of which
the first attempt would have needed to be caught locally rather than at the
regress gate:

1. Creating a *second live* DEFAULT partition without dropping the first must
   still raise 42P16 — so the test cannot become dead code.
2. **A non-default sibling child that is itself partitioned and owns its own
   DEFAULT sub-partition must not be mistaken for the parent's default.** This is
   the `part_xx_yy` shape from `insert.sql` and is the direct regression guard for
   the wrong-tree-level bug described in §4.

Gates: `go build ./...`, `go vet`, `go test ./internal/executor/`,
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`,
`scripts/tpch-spotcheck.sh` (canonical Q12=2 / Q13=35), and
`scripts/pg-regress-runner.sh --verbose insert` measured against the HEAD
baseline of 1062 lines / 58 `^+ERROR` / 57 `^-ERROR`.

## 6. Not fixed here (deferral rows filed)

The other seven `insert.sql` buckets remain — notably INSERT target-list
indirection (`col[i]`, `col.field`, ~330 lines, REFACTOR-tier), partition-bound
constraints not enforced on direct INSERT into a leaf partition, missing DETAIL
on the two partition-routing errors, and post-BR-trigger rows not re-checked.
See `.ralph/deferral_ledger.md` rows dated 2026-08-20 / M0134-0013.
