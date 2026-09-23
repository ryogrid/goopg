# Command tags: the "OK" fallback class and `SELECT n` for populating DDL

Status: landed 2026-09-23 (`6de0daf09`). Task: `.ralph/fix_plan.md` manual
M-NIGHTLY-section bug "`CREATE FUNCTION` completes with command tag `OK`".
Kind: bug.

## Finding

Every statement reaching the postmaster's generic DDL/utility dispatch gets its
CommandComplete tag from `ddlTag` (DDL plan nodes) or `utilityTag`
(`optimizer.Utility`), both in `internal/postmaster/dispatch.go`. Each is a
type switch ending in `return "OK"` — a placeholder no PostgreSQL client ever
sees. A live sweep against a private PG 18.3 found 29 statement kinds taking it:

CREATE/ALTER/DROP FUNCTION · CREATE/DROP PROCEDURE · ALTER PROCEDURE ·
ALTER ROUTINE · CREATE/DROP TRIGGER · CREATE/ALTER EVENT TRIGGER ·
CREATE/ALTER SEQUENCE · CREATE/ALTER/DROP RULE · CREATE/DROP POLICY ·
CREATE MATERIALIZED VIEW … WITH NO DATA · REFRESH MATERIALIZED VIEW ·
CREATE/ALTER/DROP PUBLICATION · CREATE/ALTER/DROP SUBSCRIPTION ·
CREATE ACCESS METHOD · ALTER OPERATOR · DO · REINDEX · CLUSTER.

The same sweep found a second, different defect in the class: PostgreSQL
completes a **populating** CREATE TABLE AS, SELECT INTO or CREATE MATERIALIZED
VIEW with `SELECT <n>` — `SetQueryCompletion(qc, CMDTAG_SELECT, es_processed)`
at `./postgres/src/backend/commands/createas.c:349` and
`./postgres/src/backend/commands/matview.c:389` — and CREATE TABLE AS … WITH NO
DATA with `CREATE TABLE AS`. goopg said `CREATE TABLE` for all three.

## Design

- `stmtCommandTag(stmt)` — one table of the 29 tags, from
  `./postgres/src/include/tcop/cmdtaglist.h` — is the fallback of **both**
  `ddlTag` and `utilityTag`, so a statement kind cannot be tagged on one route
  and not the other. `ALTER FUNCTION` reads `IsProcedure`/`IsRoutine` to emit
  ALTER PROCEDURE / ALTER ROUTINE.
- `ddlOp` counts the rows `execCreateTableAs` (CTAS and SELECT INTO share it)
  and `materializeView` write, and sets `reportProcessed` on the populating CTAS
  and CREATE MATERIALIZED VIEW paths only — REFRESH shares `materializeView` but
  keeps its plain tag, as in PG. It is exposed as
  `executor.DDLProcessedReporter`; `OpIterator` forwards it through its
  `OpAdapter` arm (the sibling of `RowsAffected`), because the postmaster holds
  the slab/tree iterator, never the bare `ddlOp`. `commandTagFor` emits
  `SELECT <n>` when it reports, else falls to `ddlTag`, where a CTAS
  (`SelectSource != nil`) tags `CREATE TABLE AS`.
- Simple- and extended-query paths both call `commandTagFor`, so no protocol
  sibling is left behind.

## Verification

Live, private PG 18.3 vs goopg, same script: all 29 kinds print PG's tag; CTAS
of 5 rows → `SELECT 5`, WITH NO DATA → `CREATE TABLE AS`, matview → `SELECT 5`
/ `CREATE MATERIALIZED VIEW`, REFRESH → `REFRESH MATERIALIZED VIEW`, SELECT INTO
→ `SELECT 5`. Pinned by 32 cases added to `TestDDLCommandTagMatchesPostgres`
and by `TestCommandTagForPopulatingDDLIsSelectN`. Gates: units, tpch-spotcheck,
SF0.25 sweep, acceptance arm, `TestPort_RegressSuite`.

## Other divergences the sweep found (filed separately)

ALTER RULE … RENAME does not rename; LOCK TABLE outside a transaction block is
accepted (PG: 25P01); REINDEX of an index-less table lacks PG's NOTICE; a
session's own NOTIFY reports PID 1; `DISCARD ALL` is a syntax error;
CREATE PUBLICATION lacks the `wal_level` WARNING.

### Follow-up fixed 2026-09-23 (`bc75b691f`): ALTER RULE RENAME / DROP RULE IF EXISTS

A rule's existence is tracked twice: the DO-NOTHING form as a
`catalog.RuleInfo` on the table (what `pg_rewrite` / `pg_get_ruledef` read) and
every rule form in the compat-object registry under `name@table` (what DROP
RULE reads). `execAlterRuleRename` renamed only the first, so DROP RULE lost
the rule; it now finds the rule in either store, checks collisions in both and
renames both (`catalog.HasCompatObject` / `RenameCompatObject`), which also makes
action-form rules renameable as PG's `RenameRewriteRule`
(`./postgres/src/backend/rewrite/rewriteDefine.c:793`) allows. `execDropRule`
answered IF EXISTS before looking the rule up, so an existing rule was skipped;
the IF EXISTS notice now fires only on a miss. A 16-statement script is
byte-identical to PG 18.3 (`TestAlterRuleRenameThenDrop`).

### Follow-up fixed 2026-09-23 (`40ae756e9`): LOCK TABLE needs a transaction block

`execLockTable` now applies PG's `RequireTransactionBlock(isTopLevel,
"LOCK TABLE")` (`./postgres/src/backend/tcop/utility.c:936`): 25P01 at top
level outside a block, before the relation is resolved; the tag is
`LOCK TABLE`. goopg's `isTopLevel` is `Context.RoutineDepth == 0`, which
exposed two prerequisites, fixed together:

- DO blocks and procedures (CALL, PL/pgSQL and SQL) ran their bodies at depth 0.
  `enterRoutineBody` (plpgsql_runtime.go) now raises the depth — with the same
  54001 stack guard as `executeStoredRoutine` — because PG runs every routine
  body through SPI, never top-level.
- PL/pgSQL could not express the statement: `lock table t;` failed "expected
  ':=' or '='". `pl_gram.y`'s `stmt_execsql` T_WORD arm treats a word not
  followed by `=`/`:=`/`[`/`.` as plain SQL; goopg's parser now does the same,
  and routes SQL-command keywords with no PL/pgSQL meaning (TRUNCATE, ANALYZE,
  VACUUM, REINDEX, CLUSTER, COPY, CHECKPOINT, MERGE, RESET, SHOW, PREPARE,
  DEALLOCATE, VALUES, WITH, TABLE) to the embedded-SQL path.

A 16-statement live script matches PG 18.3 except NOTIFY inside a DO body,
which now reaches the executor and fails (NOTIFY is postmaster-only; ledgered).

