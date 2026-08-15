# M0134-0002 — `alter_table.sql` divergence map (crash + 14 classes)

**Status:** draft
**Date:** 2026-08-15
**Task:** M0134-0002 (`.ralph/fix_plan.md`) — `postgres/src/test/regress/sql/alter_table.sql`
(CSV row `failed`, `pass_required=no`).
**Gate:** `scripts/pg-regress-runner.sh alter_table` → `tmp/regress-diffs/alter_table.diff`.

## Measured state (2026-08-15)

4668-line diff, 44 hunks. **The case crashes the goopg server mid-file**: the tail
~2091 expected lines (~45% of the diff) are lost output, not divergence. Case
composition: 3155 SQL lines, ~1705 statements, 6 `EXPLAIN`, 83 `\d`/`\d+`,
expected carries 323 `ERROR` + 56 `NOTICE` lines (~20% error-path).

## The crash (fix first — a server panic on valid SQL)

**Statement** (`alter_table.sql:1664`, the "related case (bug #17811)" block):
```sql
begin;
create temp table t1 as select * from int8_tbl;          -- 2 cols (q1,q2)
create temp view v1 as select 1::int8 as q1;             -- 1 col
create temp view v2 as select * from v1;                 -- 1 col (frozen)
create or replace temp view v1 with (security_barrier=true)
  as select * from t1;                                   -- v1 now 2 cols
create temp table log (q1 int8, q2 int8);
create rule v1_upd_rule as on update to v1 do also insert into log values (new.*);
update v2 set q1 = q1 + 1 where q1 = 123;                -- panics
```

**Panic:** `index out of range [1] with length 1` at `internal/planner/view_dml.go:168`
(`viewProxyTable`, `proxy.Columns[baseOrd].Name = names[viewOrd]`).

**Root cause.** `v2`'s defining query is a bare `SELECT *`, stored UNEXPANDED
(`v.View.Targets = [StarExpr]`). `viewAutoUpdatableChain` (`view_dml.go:44`)
calls `viewColumnMap` (`:101`) which, for a bare `*` (`:102-109`), returns
`m = [0 .. len(b.Columns)-1]` — ALL of the base relation's **current** columns.
After `CREATE OR REPLACE VIEW v1` grows v1 1→2 columns, v2's `colMap` gets 2
entries, but v2's own catalog `Columns` (frozen at creation) still has 1 entry
(`q1`) → `viewColumnNames(v2)` = `["q1"]`, and `viewProxyTable` indexes
`names[1]` out of range.

**PG behavior (oracle `alter_table.out:2655-2677`).** PG **executes** the update:
2 rows of `t1` where `q1=123` become `q1=124`, and the `v1_upd_rule` fires,
inserting 2 rows into `log`. PG freezes v2's `SELECT *` to `SELECT q1` at v2
creation; when v1 is later replaced, v2's rule is unchanged (still 1 column).
The update resolves by column NAME through the chain, so only `q1` is touched.

**Fix (PG-faithful, not a bound-check).** A `bound-check → error` would stop the
panic but diverge (PG succeeds). The correct fix makes v2's `SELECT *` map to
v2's **frozen** column list (`tbl.Columns`, already stored on the view), not the
base's current columns: `viewColumnMap`'s bare-`*` arm maps **positionally** over
`len(view.Columns)` (a bare `*` is positionally-defined — output column i = base
column i), instead of an identity map over `len(b.Columns)`. `viewAutoUpdatableChain`
passes the view table (`tbl`) in. Blast radius: view-DML chains over a `SELECT *`
view whose base gained columns — the exact bug-#17811 shape. No read-only path
affected. (The researcher's full fix — freeze the top-level `*` into explicit
targets at CREATE VIEW — also closes the read-path sibling `at_view_2` error and
reorder robustness; deferred, see the ledger.)

## Divergence classes (14, non-crash)

| # | class | goopg side | PG oracle | kind |
|---|-------|-----------|-----------|------|
| C1 | ~~`text[] \|\| text[]` operator missing~~ **LANDED** — the analyzer/planner `OpConcat` type-check rejected array operands (not a catalog gap) | `analyzeExpr` (`analyzer.go` OpConcat arm) + `exprType` (`planner.go` OpConcat arm) now accept array operands (both spellings `Name:"text[]"` and `IsArray:true`) and return the array side's type; the executor merge (`evalBinary` OpConcat) and the pg_operator seed rows (OID 349/374/375) already existed | `arrayfuncs.c` (`array_cat`/`array_append`/`array_prepend`) | executor/binding — first of two `\d+` describe blockers |
| C15 | ~~`pg_catalog.col_description(oid,int4)` builtin missing~~ **LANDED** — the executor function-name switch now has a `case "col_description":` beside `obj_description` (`internal/executor/expr.go` :9840) reading `GetComment(1259, objoid, attnum)` | `system_functions.sql:322-327` (`col_description` SQL body) + `describe.c:1986` (psql caller) | executor — second `\d+` describe blocker; 12 errors → 0, no new class, diff 4673→4677 |
| C2 | `ALTER TABLE` grammar gaps (`RENAME CONSTRAINT`, `RENAME <col> TO`, `TYPE … USING`, comma multi-action, `NO INHERIT`/`NOT VALID`, `DROP COLUMN IF EXISTS`, `SET WITHOUT OIDS`, `STORAGE`, `ANALYZE tab(col)`, ENFORCED dup) | `internal/parser/ddl.go` `parseAlterTableAction`/`parseOneAttrCmd`/`consumeAttrCmdTrailer` | `gram.y`/`tablecmds.c` | parser |
| C3 | constraint validation scans absent (ADD CHECK/PK/SET NOT NULL/VALIDATE silently accept invalid rows) | DDL path | `tablecmds.c` `ATExecAddConstraint` | correctness |
| C4 | FK semantics | DDL | `tablecmds.c` | correctness |
| C5 | btree-inet rejected | `btreeKeyTypeRejectionError` | `nbtree`/`amcheck` | executor |
| C6 | catalog gaps (pg_type array rows, pg_trigger FK rows, pg_locks) | catalog build | `pg_*.dat` | catalog |
| C7 | constraint naming/rendering (`con1` ignored, `CHECK ((a>10.2))` double-parens, partition-child index `_0_key` vs `_0_id_name_key`) | `operators_ddl.go`/explain | `tablecmds.c`/`ruleutils.c` | formatter |
| C8 | ~~system columns unmodeled (`ADD COLUMN xmin` accepted)~~ **LANDED** — a case-sensitive `isSystemColumn` helper (ctid/xmin/cmin/xmax/cmax/tableoid, no `oid`) rejects at all four entry points with 42701 + the PG-exact message | `execCreateTable`/`execCreateTableAs`/`execAlterTableAddColumn` + the RENAME arm (`operators_ddl.go`); `validatePartitionKey` reuses the helper (one name-list source). RENAME check corrected: 42P20→42701, `oid` dropped, case-sensitive | `tablecmds.c:7673` `check_for_column_name_collision` (ADD/RENAME) + `heap.c:481` `CheckAttributeNamesTypes` (CREATE/CTAS); `SysAtt[]` `heap.c:144-228` | correctness |
| C9 | inheritance semantics (inherited CHECK/NOT-NULL not enforced on children, `attinhcount` diverges) | DDL | `tablecmds.c` | correctness |
| C10 | ALTER TYPE (**data loss**: failed int8→int4 leaves table EMPTY, `internal error: expected int, got kind 1`) | `ATExecAlterColumnType` path | `tablecmds.c` | correctness |
| C11 | view-DML (`to_json` missing, `CREATE OR REPLACE VIEW` not propagating to dependents, ALTER-on-view accepted) | view_dml.go | `rewriteHandler.c` | correctness |
| C12 | message text | error msgs | `errcode()` | formatter |
| C13 | NOTICE/IF EXISTS | DDL | `tablecmds.c` | formatter |
| C14 | EXPLAIN verbosity/underline | `operators_explain.go` | `explain.c` | formatter |

## First slice

The crash fix (above) is the first slice — a server panic on valid SQL, and it
unblocks measuring the tail ~45% of the diff. Subsequent slices, in rough
dependency order: C1 (`||` operator — **LANDED 2026-08-15**, the array-op type
check), then C15 (`col_description` builtin — the second `\d+` blocker that
surfaced once C1 was fixed, **LANDED 2026-08-15**, commit `044057b9`; the 12
`col_description` errors are gone and the previously-masked describe output now
renders), then C8 (system-column name rejection — **LANDED 2026-08-15**, commit
`0f6945bc`: the case-sensitive `isSystemColumn` helper applied at all four
entry points), then C2 grammar
cluster (largest), then the correctness classes C3/C4/C9/C10/C11 each with
their own deferral rows.

## Secondary finding (corrects deferral-ledger row 1385)

The EXPLAIN `QUERY PLAN` underline width is **not** a goopg fixed-width
renderer. goopg emits no header/dash-run at all; the literal exists only as the
result-set column name (`internal/planner/plan.go:2201`). psql renders the
header+underline sized to the widest data row, so the width divergence is a
psql CLIENT symptom of a real goopg raw-indentation gap: goopg's InitPlan-subtree
lines are 4 leading columns narrower than PG's (PG `ExplainNode` `explain.c:1619-1633`
+ `ExplainSubPlans` `:4774` accumulate +6 cols/level; goopg `emitSubPlanSubtrees`
`operators_explain.go:572` `(len(detailIndent)+2)/2` + `walkPlanFiltered` `:439-442`
`2*depth` add only +2/level). Fix = +1 deeper depth for subtree descendants.
Re-verify on a deeper InitPlan before fixing (measured on a 2-level subtree).

## PG oracle citations

- `postgres/src/backend/commands/tablecmds.c` — ALTER TABLE grammar + execution
  (`ATExecAddConstraint`, `ATExecAlterColumnType`, constraint naming).
- `postgres/src/backend/commands/explain.c:1619-1633` (`ExplainNode` indent),
  `:4774` (`ExplainSubPlans`).
- `postgres/src/backend/utils/adt/arrayfuncs.c` — `array_cat`/`||`.
- `postgres/src/backend/rewrite/rewriteHandler.c` — view DML rules.
