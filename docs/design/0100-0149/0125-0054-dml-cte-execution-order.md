# 0125-0054 — data-modifying CTEs are not a prefix: PostgreSQL runs them on demand and after the main plan

**Milestone:** M0125 · **Status:** landed 2026-08-06 · **Area:** executor
(`internal/executor/operators_cte_dml.go`, `context.go`)

Successor to [0125-0053](0125-0053-dml-cte-preimage-reveal.md), which closed the
*visibility* half of data-modifying-WITH semantics and filed the *ordering* half
as the residue it deliberately did not touch.

## The defect

goopg's operator was called `cteDMLPrefixOp` and the name was the bug: it ran
every data-modifying CTE to completion before it even *built* the outer body.
PostgreSQL does the opposite. Four shapes distinguish the two orders, because
whichever sub-statement runs SECOND finds the row already stamped by this same
command and declines it (`ExecUpdate`/`ExecDelete` take the `TM_SelfModified`
arm and return NULL when `cmax == es_output_cid`):

| statement | PG 18.3 | goopg before |
|---|---|---|
| `WITH x AS (UPDATE t SET a=6 WHERE a=5 RETURNING a) DELETE FROM t WHERE a=5 RETURNING a` | `[5]`, table `[2]` | 0 rows, table `[2 6]` |
| `WITH x AS (DELETE FROM t WHERE a=1 RETURNING a) UPDATE t SET a=a+100 WHERE a=1 RETURNING a` | `[101]`, table `[2 101]` | 0 rows, table `[2]` |
| `WITH x AS (DELETE FROM t WHERE a=1 RETURNING a) DELETE FROM t WHERE a=1 RETURNING a` | `[1]`, table `[2]` | 0 rows |
| `WITH x AS (UPDATE t SET a=6 WHERE a=5 RETURNING a) UPDATE t SET a=7 WHERE a=5 RETURNING a` | `[7]`, table `[2 7]` | 0 rows, table `[2 6]` |

And with no conflict at all, only the heap order records who went first:

```sql
WITH x AS (INSERT INTO ord_log VALUES ('cte') RETURNING tag)
INSERT INTO ord_log SELECT 'outer' RETURNING tag;
-- PG 18.3: 'outer' at ctid (0,1), 'cte' at (0,2)
```

Every value above was captured on live PG 18.3 (port 65432) on 2026-08-06.

## What PostgreSQL actually does

There is no prefix phase. A data-modifying CTE becomes a `ModifyTable` node that
is *not* `canSetTag`, and it reaches execution by one of two routes
(`postgres/src/backend/executor/`):

1. **On demand.** If the main plan reads the CTE, the `CteScan` that pulls from
   it drives the `ModifyTable`. The CTE runs because something asked for its
   rows — never earlier.
2. **After the main plan.** `ExecInitModifyTable` files every non-`canSetTag`
   `ModifyTable` in `estate->es_auxmodifytables`, and `ExecPostprocessPlan`
   (`execMain.c`, called from `ExecutorFinish`) runs each of them to completion
   "in case the main query did not fetch all rows from them". That is *after*
   the main plan has finished.

So the main plan goes first, and only what nothing pulled from is swept up
behind it. The filed diagnosis in -0053 had route 2 right and did not name
route 1; both are needed, because a CTE the outer body reads obviously cannot
be deferred until after the body has produced its rows.

### The sweep runs in reverse declaration order

`ExecInitModifyTable` uses `lcons`, not `lappend`, so `es_auxmodifytables` is in
reverse initialization order and `ExecPostprocessPlan` walks it head-first.
Upstream's own comment gives the reason: a later CTE may read an earlier one,
and running the later one first lets its `CteScan` drive the earlier one instead
of finding its RETURNING rows already discarded. Confirmed live:

```sql
WITH a AS (INSERT INTO ordq VALUES ('a') RETURNING tag),
     b AS (INSERT INTO ordq VALUES ('b') RETURNING tag),
     c AS (INSERT INTO ordq VALUES ('c') RETURNING tag)
SELECT 1;
-- ctid (0,1)=c, (0,2)=b, (0,3)=a
```

and with `SELECT count(*) FROM b` as the body instead, `b` runs on demand and
the sweep then takes `c` before `a`: `(0,1)=b, (0,2)=c, (0,3)=a`.

## The implementation

`cteDMLPrefixOp.Open` no longer runs anything. It initialises the fence maps,
publishes itself on `ctx.pendingDMLCTEs`, and builds and opens the body. From
there:

- `runDMLCTE(i)` executes one CTE to completion and files its RETURNING rows
  under `Names[i]`. It is **idempotent** — a second call for an already-run CTE
  is a no-op — which is the whole reason the two routes compose.
- `materializedCTEScanOp.Open` calls `ensureCTE(name)` through
  `ctx.pendingDMLCTEs`. This is route 1.
- `runPending()` walks the CTEs in reverse declaration order and runs whatever
  is left, from `Next()` when the body reaches EOF, or from `Close()` for a body
  that was never exhausted (a cursor). This is route 2.

### Why demand-driving rather than a plan-time referenced-ness flag

The filed item proposed computing referenced-ness at plan time and running only
the referenced CTEs up front. That works, but its failure mode is silent and
wrong: a walk of the body plan that misses a subtree *undercounts* references,
the CTE gets deferred, and its `MaterializedCTEScan` reads an empty row set —
a wrong answer with no error. Demand-driving has no such direction. The scan
that would have read nothing is exactly the thing that triggers the run, so a
misjudgement is impossible rather than merely unlikely. It is also closer to
upstream, where route 1 *is* the `CteScan` pulling.

### The fence obligation the reorder creates

The fence and its mirror were gated on `ctx.InDMLCTE`, i.e. only a CTE's writes
were registered. Once the outer body runs first, its writes have to be
registered too, or a deferred CTE would see rows PG's `cmin` test hides from it:

```sql
WITH x AS (INSERT INTO fs_dst SELECT a FROM fs_src RETURNING a)
INSERT INTO fs_src VALUES (2);
-- PG 18.3: fs_dst holds [1] — the CTE never sees the outer's row 2
```

So `cteFenceInsert`, `cteFenceUpdate` and `cteFenceDelete` now gate on the
existence of the fence (i.e. "we are inside a data-modifying WITH") rather than
on the phase. `InDMLCTE` survives for the one thing that really is
phase-specific: the `CTENewToOld` → `CTESelfModifiedErrors` bookkeeping, which
exists to catch a sub-command *inside* a CTE re-modifying a CTE-written row and
must not fire for the outer body's ordinary writes.

The reveal side is symmetric for the same reason — PG's `cmax`-vs-`es_output_cid`
test does not care which sub-statement stamped the xmax — so an outer DELETE's
victims are revealed to a deferred CTE's read scans too.

## Tests

`internal/executor/cte_dml_preimage_reveal_test.go`:

- `TestCTEWriteWriteRunsOuterStatementFirst` — the four write-write shapes, the
  inverted form of -0053's `TestCTEPreImageWriteWriteDivergesFromPG`, which was
  written to be flipped.
- `TestCTEDMLRunsAfterOuterStatement` — the ctid witness with no conflict.
- `TestCTEDMLReferencedByOuterRunsFirst` — both routes in one statement: a read
  CTE runs first, an unread sibling last.
- `TestCTEDeferredDMLRunsInReverseDeclarationOrder` — the `lcons` order, both
  with and without a referenced CTE in the mix.
- `TestCTEDeferredCTECannotSeeOuterInserts` — the fence obligation above.

The six -0053 pre-image tests and the seven -0052 fence tests are unchanged and
still pass: reads are order-independent under the shared snapshot, which is
exactly why -0053 could close the visibility half without this one.

## Deferred

- **A referenced CTE is materialized in full at first demand, not streamed.**
  PG's `CteScan` pulls tuple by tuple, so an outer body that writes to the CTE's
  target table between pulls interleaves with it; goopg runs the CTE to
  completion before the first row is returned. Both end with the CTE complete
  (`ExecPostprocessPlan` guarantees it upstream), so only the interleaving is
  observable, and only for a body that writes the same rows the CTE is still
  scanning. Resume point: `runDMLCTE` in `operators_cte_dml.go` would have to
  become a pull-based operator rather than a drain loop.
- **`InDMLCTE` is a phase flag, not a command id.** The fence and the reveal
  remain a stand-in for PG's per-tuple `cmin`/`cmax` (recorded under -0052 and
  -0053). Sub-statement *count* is still not modelled: PG assigns one command id
  to the whole statement, and goopg has none at all.
