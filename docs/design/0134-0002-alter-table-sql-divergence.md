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
| C5 | ~~btree-inet rejected~~ **LANDED (2026-08-16)** — the full btree/inet_ops catalog stack (type 869, opclass 9013, opfamily 1974, amop/amproc, network_* procs, pg_operator `=`/`<`/etc.) was ALREADY seeded; the rejection was a **hardcoded Go allow-list** (`isSupportedBTreeKeyType`) missing `"inet"`/`"cidr"`, NOT an opclass lookup. Fixed: `"inet"`/`"cidr"` added to the allow-list + a new order-preserving encoder/decoder arm (`encodeInetBTreeKey`/`decodeInetBTreeKey`, fixed-width `[family][masked-network-addr][bits][full-addr]` key reproducing `network_cmp_internal` byte-wise). The expression-key gate routes through the same allow-list (no separate edit needed). | `operators_ddl.go:11364-11384` allow-list; `createBTreeIndex` :10389-10398; error :11393-11403; encoder `encodeBTreeKeyForColumn` :10981-11149 (falls to 0A000 at :11148); decoder `decodeScalarBTreeKey` `btree_scalar_keys.go:302`; expression-key gate :10370-10378 (second parallel gate) | `network.c:402-420` `network_cmp_internal` + `network_in`/`inet_in` (address-masking); `indexcmds.c` `GetDefaultOpClass` | executor (encoder/decoder arm) |
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

## C2 decomposition + first slice (2026-08-16)

Researcher decomposition (`tmp/ralph-handoffs/0134-0002-c2-grammar-research/report.md`)
split C2 into **14 sub-gaps** (11 doc-listed + 3 found new: `ADD COLUMN IF NOT
EXISTS`, `DROP CONSTRAINT IF EXISTS`, `ALTER TABLE ... OF/NOT OF` typed-table
forms), accounting for ~60 of the 88 `syntax error at or near` lines in the
diff. Root cause is structural: `parseAlterTableAction` is branch-and-`return`,
so the ALTER COLUMN / RENAME / SET-* arms return before the comma loop, and
several arms drop trailers or misuse `acceptIdentKeyword` on reserved `KwIf`.

**First slice landed (commit `8afe5bf7`): `ADD COLUMN IF NOT EXISTS`** — the
parser's ADD COLUMN arm now consumes `IF NOT EXISTS` via `acceptKeyword(KwIf)`
(mirroring the correct DROP-ATTRIBUTE pattern, NOT `acceptIdentKeyword`) and
sets `AlterTableAction.IfExists`; `execAlterTableAddColumn` emits PG's NOTICE
`column "c" of relation "r" already exists, skipping` and skips instead of
raising 42701. 8 syntax-error sites closed (88→80), NOTICE byte-exact, diff
4645→4602.

**Second slice landed (2026-08-16): `NO INHERIT` trailer** — the parser's ADD
[CONSTRAINT] CHECK arm now consumes `NO INHERIT` at both orderings (PG's
ConstraintAttributeSpec is order-independent: `check (a=2) no inherit not valid`
and trailing `NO INHERIT`) via `acceptIdentKeyword("no")`/`"inherit"` (the
column/table/NOT-NULL sibling pattern), setting `AlterTableAction.NoInherit`;
`execAlterTable` threads `act.NoInherit` into `AddCheckFull` and raises 42P16
`cannot add NO INHERIT constraint to partitioned table %q` on a partitioned
target (mirrors the CREATE TABLE sibling). 7 syntax-error sites closed (80→73);
partitioned ERROR byte-matches PG. Unmasked two C3-class gaps (ADD CONSTRAINT
CHECK without NOT VALID does not validate existing rows; INSERT/UPDATE does not
enforce CHECK at runtime) — recorded in the deferral ledger, not closed here.

**Third slice landed (2026-08-16): `RENAME CONSTRAINT`** — new parser kind
`AlterTableRenameConstraint` + `OldConstraintName` field (reusing `NewName`;
`ConstraintName` stays "ADD CONSTRAINT name") + a RENAME CONSTRAINT arm in the
RENAME branch (mirrors the ALTER DOMAIN arm); the executor `case
AlterTableRenameConstraint` renames CHECK (in-place, OID-stable, partition-child
cascade), FK (slice mutation, OID-stable), and UNIQUE/PK/EXCLUDE via
`InMemory.RenameIndex` (constraint name == index name, so the backing index
re-keys in one call) + `resyncIndexClassHeapRow` for restart durability. Error
codes byte-match PG: 42704 `constraint "%s" for table "%s" does not exist`
(pg_constraint.c:1234), 42710 `constraint "%s" for relation "%s" already exists`
for CHECK/FK (pg_constraint.c:1025), 42P07 for the index-backed path
(RenameRelationInternal, tablecmds.c:4303-4307). Closes the con2/con3/cache-pkey
rename sites + `\d` renders `"con3foo" PRIMARY KEY, btree (a)`. The `onek` block
(:294-296) stays open on a pre-existing `DROP INDEX` gap — goopg's `execDropIndex`
silently drops a constraint-backed index where PG raises 2BP01 `cannot drop
index %q because constraint %q on table %q requires it` (deferral-ledger row
appended; next slice candidate).

**Fourth slice landed (2026-08-16): `DROP INDEX` constraint-guard** —
`execDropIndex` (`operators_ddl.go:6786`) now raises 2BP01 when the target index
backs a live UNIQUE / PRIMARY KEY / EXCLUDE constraint (`idx.IsConstraint ||
idx.IsExclusion`), with the byte-exact PG message `cannot drop index %s because
constraint %s on table %s requires it` + HINT `You can drop constraint %s on
table %s instead.` (unquoted names, `getObjectDescription`,
`dependency.c:780-795`; constraint name == index name for all three kinds). A
bare `CREATE UNIQUE INDEX` (no constraint) still drops. Closes the `onek`
:294-296 `DROP INDEX`→`RENAME CONSTRAINT`→`DROP INDEX <new>` sequence (zero
`onek_unique1_constraint` occurrences in the diff).

**Fifth slice landed (2026-08-16, commit `fec178bd`): `TYPE … USING`** — researcher pass
(`tmp/ralph-handoffs/0134-0002-c2-typeusing-research/report.md`) mapped the full
path and verdicted it **bounded, not C10-sized**. Parser: the TYPE arm
(`internal/parser/ddl.go:8947-8959`) consumes `TYPE <typename>` then returns,
leaving `USING` unconsumed → the 11 `syntax error at or near (got using)` sites.
Fix mirrors the SET DEFAULT arm (`ddl.go:8875-8887`): a new `UsingExpr parser.Expr`
field on `AlterTableAction` (ast.go, beside `DefaultExpr`), parsed with
`p.parseExpr()` after `parseColumnType()`. Executor: `execAlterColumnType`
(`operators_ddl.go:21438`) already does a full per-row rebuild (Phase 1 decode →
Phase 3 truncate → Phase 4 re-encode) with a per-row `evalCast` hook at :21516
whose error is silently swallowed — the C10 data-loss root. USING rides the same
loop: resolve the expr against the OLD column schema via a new exported
`planner.ResolveAlterColumnTypeUsing` (wrapping `resolveExpr` +
`singleBindingContext`, planner.go:12396/519), evaluate per-row with
`evalExpr(plannedUsing, row, o.ctx)` (expr.go:330), coerce the result to the
target type via the existing `evalCast`, and **propagate** the coercion error
pre-rewrite instead of swallowing it. Two PG parity messages
(tablecmds.c:14495-14511): WITH-USING `result of USING clause for column "x"
cannot be cast automatically to type y` + HINT `You might need to add an explicit
cast.`; WITHOUT `column "x" cannot be cast automatically to type y` + HINT `You
might need to specify "USING x::y".` Bypass the name-unchanged no-op
(:21451-21453) when USING is present. Deferred to ledger: `SET DATA TYPE` spelling
(silent no-op), whole-row-reference rejection, generated-column rejection, typmod
threading.

**Sixth slice landed (2026-08-16): comma multi-action** — the ALTER COLUMN block
was moved out of `parseAlter`'s early-return path (`ddl.go:8778-8985`) into a new
`parseAlterColumnAction() (AlterTableAction, error)` helper, dispatched from the
top of `parseAlterTableAction` on the bare `ALTER` token, so the pre-existing
comma loop (`first := parseAlterTableAction()` then `for p.acceptSymbol(",")`)
now builds a multi-action list. Every arm converted from append+`return stmt` to
`return AlterTableAction{…}`; the no-op tail now breaks on `,` as well as `;` and
returns `AlterTableNoOp` (already ignored by `execAlterTable` at
`operators_ddl.go:7730`). No AST field and no executor change were needed —
`AlterTableStmt.Actions` is already a slice and `execAlterTable` already `for
range`s it (mutating one shared `tbl`). Closes the 7 `(got ,)` + 3
`expected ADD or DROP (got alter)` sites (both → 0). Deferral ledger row for the
sequential-apply gap (goopg mutates `tbl` per action; PG's `ATController` preps
all commands before any catalog write, so a mid-list error rolls back). Also
unmasked: `SET WITH OIDS`/`SET WITHOUT OIDS` and the `OF`/`NOT OF` typed-table
arms (`AT_OfType`/`AT_NotOf`) have no arm in `parseAlterTableAction` (already on
the remaining list).

Remaining C2 sub-gaps, ranked by error-site count ÷ risk: RENAME `<col>` TO bare
(3), ANALYZE tab(col) (4, re-route — it is an ANALYZE/VACUUM statement gap, not
ALTER TABLE), OF/NOT OF (3), NOT VALID trailer (2), STORAGE (2), DROP COLUMN IF
EXISTS (1, one-line `acceptKeyword(KwIf)`), DROP CONSTRAINT IF EXISTS (1), SET
WITHOUT OIDS (1), ENFORCED dup (1, C9-masked).

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
