# 0125-0053 — the rest of a statement still sees the pre-image of a row its own DML CTE removed

**Milestone:** M0125 · **Status:** landed 2026-08-06 · **Area:** executor
(`internal/executor/operators_cte_dml.go`, `context.go`, `operators_storage.go`,
`operators_index.go`, `operators_indexonly.go`, `operators_merge.go`,
`operators_upsert.go`, `internal/server/dispatch.go`)

Successor to [0125-0052](0125-0052-dml-cte-write-fence-covers-whole-statement.md),
which gave the DML-CTE write fence its missing half.

## The defect

```sql
CREATE TABLE dz(a int); INSERT INTO dz VALUES (1),(2);
WITH x AS (DELETE FROM dz WHERE a = 1 RETURNING a) SELECT count(*) FROM dz;
```

PG 18.3 answers **2**, goopg answered **1**. And with an UPDATE:

```sql
-- dz now holds {2, 5}
WITH x AS (UPDATE dz SET a = 6 WHERE a = 5 RETURNING a) SELECT a FROM dz ORDER BY a;
```

PG answers **[2, 5]**, goopg answered **[2]**.

The write fence can only ever *skip* rows. PostgreSQL's rule is symmetric — "the
sub-statements … can't see one another's effects on the target tables" covers
deletions too — and goopg had no mechanism for the removal direction: a CTE
DELETE stamps xmax with our own XID, and a CTE UPDATE does the same to the old
version whose new version is then fenced, so the row simply vanished.

## Why PostgreSQL needs no second mechanism

The same `es_output_cid` that hides a sibling's insert through `cmin` reveals
the pre-image through `cmax`. `HeapTupleSatisfiesMVCC`
(`postgres/src/backend/access/heap/heapam_visibility.c`) treats a tuple deleted
by the *current command* as still visible, and only a tuple deleted by an
*earlier* command of the same transaction as gone. goopg's heap has no
per-tuple command id, so `ctx.CTEXmaxReveal` stands in for the cmax test — the
mirror image of `ctx.CTEWriteFence`, keyed by the same `CTEFencePtr{Rel, Ptr}`.

## What landed

`cteFenceDelete` registers a tuple wherever a DML CTE stamps xmax: the two
`deleteOp` paths (`Next`, `deleteWithUsing`), MERGE's `WHEN MATCHED THEN
DELETE`, and — via `cteFenceUpdate`'s old key — every UPDATE path, since an
update's old version is a removal as far as the rest of the statement is
concerned.

**Only read scans consult it.** That split is not a shortcut, it is PG's own
structure: `ExecDelete`/`ExecUpdate` take the `TM_SelfModified` arm for such a
tuple and, when `cmax` equals `es_output_cid`, return NULL without touching the
row — "already deleted by self; nothing to do" (`nodeModifyTable.c`). A DML
target scan that simply does not find the row yields the same row count and the
same heap state, so `scanMatching`, `updateViaIndex`, the EPQ chain walkers and
the ON CONFLICT arbiter probe all pass a nil reveal.

The reveal lives **inside** the HOT-chain walk, not on its result. The
pre-image sits ahead of the CTE's new version in the chain; testing only the
returned tuple would hand back the new version, which the write fence then
drops, losing the row entirely. `followHOTChain`/`followHOTChainNoCopy` take a
`cteRevealFn` (nil for every write path, and nil-by-construction for any
statement without a data-modifying WITH, so the ordinary scan path stays
allocation-free).

## The correction that the isolation suite forced

The first implementation treated membership in `CTEXmaxReveal` as a licence to
show the tuple. That broke `TestPort_IsolationInsertConflictDoUpdate3`, which
returned a **duplicate** row for key 1.

PG relaxes only the *cmax arm* — the xmin snapshot test still has to pass — and
the difference is not academic. `INSERT … ON CONFLICT DO UPDATE` carries a
documented MVCC violation that lets it update a tuple *not visible to the
command's snapshot* (the header comment of
`postgres/src/test/isolation/specs/insert-conflict-do-update-3.spec` spells this
out). So a CTE upsert can stamp a version this statement was never allowed to
see, and forcing it visible returned it *alongside* the snapshot-visible
version of the same row.

`cteRevealHeader` therefore re-runs the ordinary visibility test on a copy of
the header with `Xmax` cleared. Clearing Xmax alone is sufficient:
`TupleVisible`/`TupleVisibleSubxact` reach no xmax infomask bit once Xmax is
invalid.

## What this does NOT close — the execution-order discovery

While capturing the oracle, PG revealed a second, independent divergence:

```sql
WITH x AS (INSERT INTO ord_log VALUES ('cte') RETURNING tag)
INSERT INTO ord_log SELECT 'outer' RETURNING tag;
-- PG 18.3: 'outer' at ctid (0,1), 'cte' at (0,2)
```

**PG runs the main plan first**, then runs any not-yet-completed data-modifying
CTE in `ExecPostprocessPlan` (`postgres/src/backend/executor/execMain.c`).
goopg's `cteDMLPrefixOp` runs every DML CTE *before* building the outer body.

Reads are unaffected — that is precisely why they are the defined half — but
when the outer statement **writes** the same row a CTE wrote, the two orders
give different answers, because whichever ran second finds the row already
stamped and declines it:

| shape (all captured on live PG 18.3) | PG | goopg |
|---|---|---|
| outer `DELETE` of a row the CTE UPDATEd | `DELETE 1`, table `[2]` | 0 rows, table `[2 6]` |
| outer `UPDATE` of a row the CTE DELETEd | `UPDATE 1`, table `[2 101]` | 0 rows, table `[2]` |
| outer `DELETE` of a row the CTE DELETEd | `DELETE 1` | 0 rows (table agrees: `[2]`) |
| outer `UPDATE` of a row the CTE UPDATEd | `UPDATE 1`, table `[2 7]` | 0 rows, table `[2 6]` |

The `WITH` documentation calls this case unpredictable, so goopg's answers are
defensible — but they are not PG's. `TestCTEPreImageWriteWriteDivergesFromPG`
pins the current behaviour so a later loop sees it flip rather than
rediscovering it; filed as the successor item with a deferral-ledger row.

## Verification

Six new tests in `internal/executor/cte_dml_preimage_reveal_test.go`, every
expectation captured from live PG 18.3 on port 65432 before the fix was
written: both filed witnesses, the pre-image **content** check (old column
values, not just the old key), the index-scan plan shape, the outer
`INSERT … SELECT` read, the sibling-DELETE no-op, and the write-write residue.

Gates: the seven 0125-0052 tests unchanged; `internal/executor`,
`internal/server`, `internal/planner`, `internal/analyzer`, `internal/mvcc`,
`internal/storage`; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`;
the full `TestPort_Isolation*` suite (only the pre-existing
`TestPort_IsolationEvalPlanQual` failure remains — nightly
`AI-20260806-011323-001`, confirmed failing at HEAD without this change);
`scripts/tpch-spotcheck.sh` Q12=2/Q13=35; `scripts/tpcds-sf05-regression.sh
plans` → `queries=99 same=99 changed=0`.
