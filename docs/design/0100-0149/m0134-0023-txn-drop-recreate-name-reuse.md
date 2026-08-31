# Same-transaction DROP + recreate of a relation name (M0134-0023)

Status: accepted
Case: `postgres/src/test/regress/sql/write_parallel.sql` (regress-sql, was `not-tried`)

## Measurement at HEAD

`scripts/pg-regress-runner.sh --verbose write_parallel` **FAILS** — the
`not-tried` status resolved to genuinely failing, so the row stays `failed`
(it is not a stale status). Diff: **86 total lines / 12 `^+ERROR` / 0
`^-ERROR`**.

Exact accounting of all 86 lines (measured, not estimated: 3 metadata + 27
unchanged context + 56 changed):

| class | lines | note |
|---|---|---|
| (i) the single false `42P07` collision | **1** | diff line 41 — the point divergence |
| (ii) `25P02` cascade lines that fully resolve once (i) is fixed | **9** | each verified to succeed standalone |
| (iii) permanently parallel-plan-shaped `EXPLAIN` mismatches | **46** | 4 blocks; 12+12 measured directly, 11+11 derived from the measured sibling shape |

1 + 9 + 46 = 56 changed; 56 + 27 + 3 = 86. Consistent.

## The structural ceiling — why this case is PARKED, not fixable

44 of the 80 expected-output lines (**55%**) are `Finalize HashAggregate ->
Gather -> Partial HashAggregate -> Parallel Seq Scan` plan shapes across four
`EXPLAIN (COSTS OFF)` blocks. goopg has **no parallel-worker execution path at
all**, so these can never match — the same hard blocker that parked
M0134-0008 (`select_parallel`). Even a fully correct EXPLAIN-of-CTAS unwrap
could only ever emit goopg's *serial* `HashAggregate -> Seq Scan on tenk1`.
No fix in this loop can flip the row; the case is PARKED with a re-arm
trigger on a parallel-query milestone.

All six non-EXPLAIN features the case exercises were verified to **already
work** standalone: CTAS with `GROUP BY`, `SELECT ... INTO`, `CREATE` /
`REFRESH` / `REFRESH ... CONCURRENTLY MATERIALIZED VIEW`, `CREATE UNIQUE
INDEX` on a matview, and `PREPARE` + `CREATE TABLE ... AS EXECUTE`. `tenk1`
loads correctly. The case's failure is therefore **not** a feature gap.

## The real defect — and why it is worth shipping anyway

goopg raises a false `42P07 relation "parallel_write" already exists` on the
standard PG regress idiom **create → drop → recreate the same name inside one
transaction**. That single error aborts the transaction and cascades `25P02`
into every following statement (11 of the 12 `+ERROR` lines).

The value of fixing it is **cross-case, not local**: create/drop/recreate is a
pervasive idiom in the upstream regress suite, so this defect is silently
inflating `+ERROR`/cascaded-abort counts in other not-yet-run cases.

### Root cause

goopg's catalog is one shared in-memory structure with **no per-session MVCC
visibility** (see `pg_class is virtual`). DROP inside an explicit transaction
therefore cannot remove the catalog row; it queues a `PendingTableDrop` and
applies it at COMMIT (`operators_ddl.go:6867-6892` `dropTableByRef` /
`allowDefer`; `session.go:891/899`; `operators_tx.go:186`
`ApplyPendingTableDrops`). This deferral is **deliberate and already
documented twice** — `docs/design/0118-0074-drop-index-deferred-removal-until-commit.md`
and `docs/design/0118-0081-drop-table-deferred-inheritance-skip.md` — because
it is what provides (1) cross-session visibility-until-commit (PG:
`RangeVarCallbackForDropRelation` / `performDeletion`) and (2) ROLLBACK
restore (nothing was ever removed, so discarding the pending list suffices).

0118-0081's own "Known limitation" section already records the same-session
half of this gap for `SELECT`. This loop's finding is the **`CREATE` side of
that identical accepted limitation**.

PG has no such problem: the name-collision check in
`postgres/src/backend/catalog/heap.c:heap_create_with_catalog` runs against
the ordinary syscache/MVCC snapshot, so a tuple this same transaction already
deleted is simply invisible to it (`tablecmds.c:RemoveRelations` does a plain
`CatalogTupleDelete`).

**Therefore eager removal is a REGRESSION, not a fix.** The correct shape is a
*visibility* change: the pending-dropped name must be invisible to
same-session lookups while the row stays physically present for other sessions
and for rollback-restore.

## The load-bearing finding — the fix is THREE pieces, and any subset is worse than nothing

The obvious one-site patch (teach the existence check at
`operators_ddl.go:1765` to skip a pending-dropped name) is **not merely
incomplete — it is actively dangerous**. Three pieces are required and they
must land together:

1. **Visibility.** `execCreateTable`'s check (`operators_ddl.go:1765`, shared
   by CREATE TABLE / CTAS / SELECT INTO / matview) must treat a name held in
   the session's `PendingTableDrop` list as absent.

2. **The catalog guard.** Piece 1 alone still fails, because
   `InMemory.CreateTable` (`catalog.go:12193-12214`) has its **own**
   duplicate-key guard — it is not a bare `ns.tables[k] = t`. Since the
   deferral deliberately leaves the old table in the map slot, the low-level
   create would reject the insert anyway. The guard must permit the overwrite
   when the session holds a matching pending drop.

3. **Both transaction exits.** Piece 2 clobbers the map slot, which breaks
   *both* exits unless handled:
   - **COMMIT landmine.** `ApplyPendingTableDrops` →
     `dropTableByRefImmediate` removes **by name**
     (`operators_ddl.go:6949`; `catalog.go:20262-20281` is a pure name-keyed
     map delete with no OID/identity check). After a recreate, COMMIT would
     delete the **freshly created replacement**. The pending drop must be
     cancelled when the name is recreated.
   - **ROLLBACK landmine (symmetric).** `rollbackDDLCreate`
     (`operators_tx.go:295-334`) undoes an in-txn CREATE with a bare
     `Catalog.DropTable(entry.Name, dbOid)` — it deletes by name and has no
     notion of what the CREATE shadowed. So `BEGIN; DROP TABLE t; CREATE
     TABLE t(...); ROLLBACK;` would leave the slot **empty**, losing the
     original already-committed `t`.

   Fix: `DDLUndoEntry` (`session.go:50-54`) gains a `ShadowedTable
   *catalog.Table` field carrying the pointer the CREATE displaced (the
   pointer is already captured on `PendingTableDrop.Table` and stays valid —
   Go pointers survive map mutation). `rollbackDDLCreate` branches on it and
   **restores** via the existing primitive `InMemory.RegisterTable`, already
   used for exactly this purpose at the savepoint-DDL-drop undo path
   (`operators_tx.go:362`). The closest existing precedent to mirror is
   `TempTableShadows` (`operators_ddl.go:1771-1801`), which restores a
   permanent table shadowed by a same-named TEMP table.

**Why this matters more than the diff count:** `write_parallel.sql` ends in
`rollback;` and never commits, so the COMMIT landmine is *invisible to this
test's pass/fail*. Shipping the visibility half alone would look perfectly
clean here while planting a silent data-loss bug that only surfaces in a
sibling case that COMMITs. This is the eighth consecutive loop in which
interrogating the researcher's first recommendation changed the work
materially — here it converted a "one-site contained fix" into a three-piece
change and caught two symmetric data-loss paths that the case's own diff
cannot detect.

## Scope

**In scope:** the table path (`execCreateTable`, covering CREATE TABLE, CTAS
and `SELECT INTO`).

**Correction made during implementation.** This document originally asserted
that `CREATE MATERIALIZED VIEW` routes through the same `execCreateTable`
check (reasoning from `IsMatView` being a post-creation `*catalog.Table`
field). That is **wrong**: matviews are a distinct AST node
(`CreateMatViewStmt`) dispatched to a separate `execCreateMatView` with its
own existence check and its own `CreateTable` call. A matview drop+recreate in
one transaction therefore still hits the false `42P07`. Ledgered as a
follow-up alongside CREATE INDEX / CREATE VIEW rather than scope-crept into —
the same discovery class as the four-gate finding in M0134-0022: an
"N affected sites" count is a lower bound until a guard test proves each one.

**Deliberately out of scope, ledgered:** `CREATE MATERIALIZED VIEW`
(`execCreateMatView`, per the correction above), `CREATE INDEX`
(`operators_ddl.go:7113/:7117`, via `LookupIndex`) and `CREATE VIEW`
(`operators_ddl.go:5925-5926`) have the structurally identical blind spot —
DROP INDEX's deferral doc 0118-0074 documents the same same-session gap — but
each needs its own pending-drop plumbing and undo wiring, which would double
the blast radius of a catalog-level change. `CREATE SEQUENCE`
(`operators_ddl.go:18395-18398`) is **not** affected: it consults a separate
`LookupSequence` registry.

## Non-goal: EXPLAIN of CTAS

`EXPLAIN (COSTS OFF)` on CTAS / `SELECT INTO` renders the wrapper node
(`operators_explain.go:2003`: `case *optimizer.DDL: return fmt.Sprintf("DDL
%T", p.Stmt)`) instead of unwrapping to the inner SELECT's plan, as PG does in
`postgres/src/backend/commands/explain.c:ExplainOneUtility`. Fixing it yields
**NET ~0** for this case — a textbook gates-in-series result: goopg's serial
plan still mismatches PG's parallel plan, so it changes *which* lines differ,
not *whether* they differ. Ledgered, not shipped.

## What shipped, and the measured result

All three pieces landed together, as required:

1. **Visibility** — `execCreateTable`'s existence check
   (`internal/executor/operators_ddl.go`) treats a name held in the session's
   `PendingTableDrop` list as absent.
2. **Catalog opt-in** — new `InMemory.CreateTableReplacingPendingDrop`
   (`internal/catalog/catalog.go`). The existing `InMemory.CreateTable`
   duplicate-key guard is **unchanged**: the overwrite is an explicit opt-in
   for this one caller, so a genuine duplicate `CREATE` still raises `42P07`.
3. **Both exits** — `DDLUndoEntry.ShadowedTable` + new
   `BasicSession.CancelPendingTableDropMatching` (`internal/executor/session.go`);
   a new `ddlOp.pendingDropShadow` field and `createTableHonoringPendingDrop`
   helper wired into the direct CREATE TABLE path and **both** CTAS call sites
   in `execCreateTableAs`; `rollbackDDLCreate`
   (`internal/executor/operators_tx.go`) restores the shadowed table via
   `InMemory.RegisterTable` instead of leaving the slot empty.

**Result: `write_parallel` 86 → 80 diff lines, `^+ERROR` 12 → 0.** Every error
in the case is eliminated; the entire residual 80 lines is the parallel-plan /
`DDL *parser.CreateTableStmt` EXPLAIN shape, which is the structural ceiling
described above. The case stays **PARKED** and its CSV row stays `failed`.

Guard tests (`internal/executor/txn_drop_recreate_test.go`, all FAIL-pre
verified via `git stash`): `TestTxnDropRecreate_RollbackAfterNoCommit`,
`TestTxnDropRecreate_CommitLandmine`, `TestTxnDropRecreate_RollbackLandmine`,
`TestTxnDropRecreate_NoRegression` (subtests `DropThenRollbackNoRecreate`,
`PlainDuplicateStill42P07`), `TestTxnDropRecreate_CTAS`.

The two landmine tests are the point of the slice: because
`write_parallel.sql` ends in `rollback;`, the case's own diff would have looked
identical had only piece 1 shipped — while a silent data-loss bug sat waiting
for the first sibling case that COMMITs.
