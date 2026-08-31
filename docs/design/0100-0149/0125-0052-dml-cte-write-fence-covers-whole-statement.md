# 0125-0052 — a data-modifying CTE's writes are invisible to the *whole* statement, not just to an outer SELECT

**Milestone:** M0125 · **Status:** landed 2026-08-06 · **Area:** executor
(`internal/executor/operators_cte_dml.go`, `operators_storage.go`,
`operators_index.go`, `operators_indexonly.go`, `operators_merge.go`,
`operators_upsert.go`, `context.go`)

## The defect

```sql
CREATE TABLE dm15(a int);
INSERT INTO dm15 VALUES (1);
WITH x AS (INSERT INTO dm15 VALUES (15) RETURNING a)
DELETE FROM dm15 WHERE a = 15 RETURNING a;
```

PG 18.3 returns **0 rows** and leaves `{1, 15}` in the table. goopg returned
**1 row** and emptied it: the outer DELETE deleted the row its own CTE had just
inserted.

The same wrong answer appeared in four more shapes, all verified against live
PG 18.3 (port 65432) before the fix was written:

| shape | PG 18.3 | goopg (pre-fix) |
|---|---|---|
| outer `DELETE` of the CTE's row | 0 rows, row survives | 1 row, table emptied |
| outer `UPDATE` of the CTE's row | `UPDATE 0`, row unmodified | `UPDATE 1`, row became 116 |
| outer `INSERT … SELECT` reading the target | copies the 2 pre-statement rows | copied 3, including the CTE's |
| sibling CTEs (`x` inserts, `y` deletes the same key) | `y` matches nothing, row survives | `y` deleted x's row |
| outer `SELECT count(*)` | 1 (pre-statement) | 2 |

The last row is the one that matters for diagnosis: the outer-SELECT half was
believed to work. It did not.

## Why PostgreSQL gets this right

Every sub-statement of a data-modifying `WITH` runs under the same
`estate->es_snapshot` **and** the same `estate->es_output_cid`
(`postgres/src/backend/executor/execMain.c:InitPlan`), so a sibling's tuple
fails the `cmin >= curcid` test in `HeapTupleSatisfiesMVCC`
(`access/heap/heapam_visibility.c`). The user-visible rule is in the `WITH`
documentation: "the sub-statements … can't see one another's effects on the
target tables".

goopg's heap has no per-tuple command id, so it stands in a **write fence**:
`cteDMLPrefixOp.Open` records every tuple the DML CTEs write, and later scans
skip those tuples. The mechanism was already there. Two things were wrong with
it.

## Root cause 1 — plain `INSERT` never joined the fence

Only `upsertOp` (`ON CONFLICT`) and the three UPDATE paths registered their
output pointers. `insertOp` — the overwhelmingly common DML-CTE body — wrote
its rows and registered nothing, so the fence was *empty* for the archetypal
`WITH x AS (INSERT …)`. Every consult site then found nothing to skip. This is
why the outer-SELECT half failed too: it was never exercised by an INSERT.

`MERGE`'s insert action had the same hole.

## Root cause 2 — the fence key was not relation-qualified

The fence was keyed by `storage.ItemPointer` alone. `{block 0, offset 1}` is
the first row of *every* table, so registering INSERTs — which land at exactly
those low pointers — would have made

```sql
WITH x AS (INSERT INTO fa VALUES (1) RETURNING a) DELETE FROM fb WHERE a = 7;
```

skip `fb`'s row because `fa`'s new row shares its pointer. The collision was
already known: the EvalPlanQual site carried a comment ("Verify xmin ==
currentTx to avoid false positives when another table's CTE-written rows
coincidentally share the same {block,slot}") and re-read the tuple to
disambiguate. Fixing root cause 1 without fixing this would have traded one
wrong answer for another.

## The fix

1. **`CTEFencePtr{Rel, Ptr}`** replaces the bare `ItemPointer` as the key of
   `Context.CTEWriteFence`, `CTENewToOld` and `CTESelfModifiedErrors`. The
   EvalPlanQual xmin re-read is deleted — the key now disambiguates.
2. Three helpers in `operators_cte_dml.go` replace five copies of the same
   inline block: `cteFenceInsert`, `cteFenceUpdate` (which also carries the
   self-modified bookkeeping, and now takes separate source/destination
   relations so a cross-partition move keys each version to its own partition)
   and `cteFenced`.
3. `cteFenceInsert` is called from both `insertOp` write sites (partitioned and
   plain) and from `MERGE`'s insert action.
4. Every heap-reading scan consults the fence, not just `seqScanOp`:
   `indexScanOp`, `indexOnlyScanOp` and the index-driven UPDATE fast path in
   `operators_storage.go`. Otherwise the answer depended on which scan the
   planner picked — with a primary key on the target, the pre-fix outer SELECT
   went through an Index Only Scan and returned the CTE's row even where the
   seq-scan path would have hidden it.

`scanMatching` — the scan behind an outer UPDATE/DELETE — already consulted the
fence, so no new call site was needed there; it was starved of entries, not
missing the check.

## What is still not PG-faithful (deferred)

The fence hides rows the CTE **added**. It cannot show rows the CTE
**removed**, which PG's `cmax` test does:

| shape | PG 18.3 | goopg (post-fix) |
|---|---|---|
| `WITH x AS (DELETE … RETURNING a) SELECT count(*) FROM t` | 2 (pre-delete) | 1 |
| `WITH x AS (UPDATE t SET a=6 WHERE a=5 …) SELECT a FROM t` | `[2, 5]` — old version visible | `[2]` — row vanishes |

A CTE DELETE stamps `xmax` with our own XID, and a CTE UPDATE does the same to
the old version while the new version is fenced, so the row disappears from the
rest of the statement entirely instead of showing its pre-image. Closing this
needs the inverse of the fence (a pre-image set the visibility check consults)
or, faithfully, a per-tuple command id. Filed as **M0125-0053** with a ledger
row dated 2026-08-06.

Also unchanged: PG's execution *order* differs from goopg's. PG runs an
unreferenced data-modifying CTE in `ExecPostprocessPlan` (after the outer
query), while goopg's `cteDMLPrefixOp` always runs the CTEs first. Under the
shared-snapshot rule the two orders agree on every shape tested above, and the
`WITH` documentation calls the outcome of two sub-statements modifying the same
row unpredictable — but the orders are not interchangeable in general (a
sub-statement that would raise a constraint violation may raise it at a
different point). Recorded in the ledger, not fixed.

## Verification

- `internal/executor/cte_dml_outer_dml_fence_test.go` — seven tests, one per
  shape in the tables above, each pinned to a value captured from live PG 18.3
  on 2026-08-06 (outer DELETE / UPDATE / INSERT…SELECT / SELECT, sibling CTEs,
  index-scan plan shape, cross-relation non-collision).
- `internal/executor`, `internal/planner`, `internal/analyzer`,
  `internal/parser`, `internal/server` suites.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`.
- `scripts/tpch-spotcheck.sh` (Q12=2 / Q13=35) — the fence touches the seq-scan
  and index-scan hot paths, so the row-count tripwire is mandatory.
- `scripts/tpcds-sf05-regression.sh plans` — no plan-shape change expected.
