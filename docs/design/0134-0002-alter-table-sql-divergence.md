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
| C7 | constraint naming/rendering — **`con1` ignored: LANDED (2026-08-18)**, see the "C7 slice 1" section below; still open: `CHECK ((a>10.2))` double-parens, partition-child index `_0_key` vs `_0_id_name_key` | `internal/parser/ast.go`/`ddl.go` + `operators_ddl.go` `execCreateTable`; rendering side still `operators_ddl.go`/explain | `parse_utilcmd.c` `transformCheckConstraints` + `heap.c` `ChooseConstraintName`; rendering `ruleutils.c` | parser+executor (naming) / formatter (rendering) |
| C8 | ~~system columns unmodeled (`ADD COLUMN xmin` accepted)~~ **LANDED** — a case-sensitive `isSystemColumn` helper (ctid/xmin/cmin/xmax/cmax/tableoid, no `oid`) rejects at all four entry points with 42701 + the PG-exact message | `execCreateTable`/`execCreateTableAs`/`execAlterTableAddColumn` + the RENAME arm (`operators_ddl.go`); `validatePartitionKey` reuses the helper (one name-list source). RENAME check corrected: 42P20→42701, `oid` dropped, case-sensitive | `tablecmds.c:7673` `check_for_column_name_collision` (ADD/RENAME) + `heap.c:481` `CheckAttributeNamesTypes` (CREATE/CTAS); `SysAtt[]` `heap.c:144-228` | correctness |
| C9 | inheritance semantics (inherited CHECK/NOT-NULL not enforced on children, `attinhcount` diverges) | DDL | `tablecmds.c` | correctness |
| C10 | ~~ALTER TYPE (**data loss**: failed int8→int4 leaves table EMPTY, `internal error: expected int, got kind 1`)~~ **LANDED (2026-08-16)** — static assignment-coercibility gate `canAssignCast` (int2/int4/int8→bool + text→int rejected) at the top of `execAlterColumnType`, no-USING path, before storage work; the crash itself was already closed by C2 slice 5 (`fec178bd`). | `ATExecAlterColumnType` path | `tablecmds.c` | correctness |
| C11 | **SPLIT (2026-08-17)** into C11a/C11b/C11c — see the "C11 decomposition" section below; the original cell was wrong on two counts (`internal/executor/view_dml.go` does not exist, and the `CREATE OR REPLACE VIEW` symptom is a missing SQL deparser, not a propagation gap). **C11a LANDED**: ALTER TABLE structural actions on a view now raise 42809 + `DETAIL: This operation is not supported for views.` via `viewAllowedAlterAction`/`alterActionName` + an all-actions pre-scan in `execAlterTable` (`operators_ddl.go`). C11b (`to_json` family) and C11c (deparser) deferred with ledger rows. | `internal/executor/operators_ddl.go` `execAlterTable`; C11b `internal/executor/expr.go` fn switch; C11c `execCreateView` :5244 + `expr.go` :8242-8292 | `tablecmds.c:6739` `ATSimplePermissions` + `pg_class.c:24` `errdetail_relkind_not_supported`; C11b `json.c` `to_json`; C11c `view.c` `DefineVirtualRelation` + `ruleutils.c` `get_query_def` | correctness |
| C12 | message text | error msgs | `errcode()` | formatter |
| C13 | NOTICE/IF EXISTS | DDL | `tablecmds.c` | formatter |
| C14 | EXPLAIN verbosity/underline | `operators_explain.go` | `explain.c` | formatter |
| C16 | **ownership/ACL checks absent entirely** — `must be owner of ...` is never raised for tables/indexes/views (only `database_ddl.go` has any owner check); goopg happily executes DDL the regress script expects to be refused | RENAME family `operators_ddl.go:7729-7749` (index) + table/view RENAME paths — no ownership verification anywhere; only an owner-*stamping* helper at `operators_ddl.go:936` | `tablecmds.c:6795` `ATSimplePermissions` → `object_ownercheck` (`aclchk.c`) | correctness — **own milestone**; cheapest slice = the RENAME family only (3–4 call sites behind one shared helper) |
| C17 | **`pg_locks` always empty** — largest single class (~180 changed lines). NOT a view-rendering or lock-machinery gap: the txn-scoped lock machinery (`context.go:1279-1298`) and the view renderer (`relation_locks.go:80-183`) are both correct. Most ALTER TABLE sub-actions simply never acquire a lock | only DROP/ATTACH/OWNER-TO call `acquireDDLLockTxn`/`acquireRelLockTxn` (`operators_ddl.go:6207,6300,7229,7293,7347,7617`); SET STATISTICS, CLUSTER ON, reloptions, SET STORAGE/DEFAULT, ADD FK, VALIDATE CONSTRAINT, CREATE TRIGGER acquire nothing | `tablecmds.c:4608` `AlterTableGetLockLevel`; `lock.c` `GetLockStatusData` | correctness — **own milestone**; first cheap sub-slice = one blanket `AccessExclusiveLock` acquire at the top of `execAlterTable` (fixes row *existence*, not the exact per-action `mode` strings) |
| C18 | **EXPLAIN `Append` / constraint exclusion** — a *planner* gap, not C14 verbosity. Three bundled issues: no constraint exclusion at all (the Go `ConstraintExclusion` GUC is a dead stub), missing per-child `<parent>_N` alias, and Filter hoisted onto the Append instead of repeated per child | planner inheritance expansion; no `relation_excluded_by_constraints` equivalent exists | `plancat.c relation_excluded_by_constraints`; `prepunion.c expand_inherited_rtentry` | planner — **own milestone** (needs a real optimizer pass) |
| C19 | ~~`\d+` describe drift~~ **LANDED (2026-08-18)** — see the "C19 — LANDED" section below. **The premise was inverted.** goopg *over*-produces the `Compression` column and the `Access method: heap` footer because `pg_regress` always invokes psql with `-v HIDE_TABLEAM=on -v HIDE_TOAST_COMPRESSION=on` (`pg_regress_main.c:74-79`) and goopg's own runner never passes them — a **harness** bug worth ~8 hunks suite-wide, not an engine gap. The one genuine engine bug in the class: `pg_get_indexdef` ignores its 2nd/3rd arguments and always returns the whole `CREATE INDEX` statement | harness: `scripts/pg-regress-runner.sh:214`; engine: `internal/executor/expr.go:9469-9491` (`pg_get_indexdef` arity) | `pg_regress_main.c:74-79`; psql caller `describe.c:1936-1937` (3-arg per-column form); `ruleutils.c:1270` `pg_get_indexdef_worker`, `colno`-gated branches :1417-1460 | harness + executor — **the cheapest real slice of the four** |

## Residual re-characterisation (2026-08-18) — C16–C19

After the C7 slice-1 landing the residual `alter_table` diff (4048 lines / 107
hunks at `f8284ae2`) is **no longer dominated by the C7/C12/C13/C14 formatter
tail**. Four classes outside the original 14-class frame carry most of it; they
are filed above as C16–C19. Two conclusions govern the next slices:

1. **C16, C17 and C18 are each milestone-sized**, not slices. C16 needs an
   ownership-check layer goopg does not have at all; C17 needs per-action lock
   levels across the whole ALTER TABLE surface; C18 needs a constraint-exclusion
   optimizer pass. Filing them here is the deliverable — do not attempt them as
   `alter_table.sql` slices.
2. **C19 is the reachable one, and half of it is our own harness.** goopg's
   regress runner diverges from upstream `pg_regress`, which sets
   `HIDE_TABLEAM=on` and `HIDE_TOAST_COMPRESSION=on` on every psql invocation
   (`postgres/src/test/regress/pg_regress_main.c:74-79`). Because those are
   *client-side* psql variables consumed by `describe.c`, passing them costs
   nothing in the engine and corrects the comparison for the whole suite, not
   just `alter_table`. The remaining engine half is `pg_get_indexdef`: psql's
   `\d+` calls the three-argument per-column form
   (`describe.c:1936-1937`), and PG's `pg_get_indexdef_worker`
   (`ruleutils.c:1270`, `colno`-gated branches :1417-1460) returns just that
   column's key expression when `colno != 0`, whereas goopg's implementation
   (`internal/executor/expr.go:9469-9491`) discards `colno`/`pretty` and always
   emits the full statement.

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

**Seventh slice landed (2026-08-16, commit `e4395f7d`): `DROP COLUMN`/`DROP
CONSTRAINT` IF EXISTS** — both arms of `parseAlterTableAction` now consume `IF
EXISTS` via `acceptKeyword(KwIf)`/`acceptKeyword(KwExists)` and set the existing
`AlterTableAction.IfExists` flag (the DROP COLUMN arm had never actually consumed
it — its old `acceptIdentKeyword` call only matched `TokenIdent`, never the
`KwIf`/`KwExists` keyword tokens, so `DROP COLUMN IF EXISTS` was a syntax error;
the slice-1 comment at `ddl.go:9701` already documented this trap).
`execAlterDropColumn` (`operators_ddl.go:21278`) and `execAlterTableDropConstraint`
(`:10011`) emit PG's NOTICE and `return nil` when the object is missing (`column
%q of relation %q does not exist, skipping` / `constraint %q of relation %q does
not exist, skipping`, byte-exact; ATExecDropColumn tablecmds.c:9326-9328 /
ATExecDropConstraint :14060-14062). The drop-constraint skip fires only at the
`pkIdx == nil` fall-through (after all five kinds miss), never at a single-kind
miss. Closes the `drop column if exists non_existing` + `drop constraint IF EXISTS
anothertab_chk` divergence lines (8 → 0).

**Eighth slice landed (2026-08-16): `RENAME <col> TO` bare form** — the parser's
RENAME arm required `acceptKeyword(KwColumn)` (mandatory), so the bare `RENAME a
TO b` form fell through to `expectKeyword(KwTo)` and errored `expected keyword to
(got a)` (gram.y:9974 `opt_column: COLUMN | /*EMPTY*/` proves COLUMN optional).
Reordered the arm: RENAME CONSTRAINT (unchanged) → RENAME VALUE no-op (unchanged)
→ `RENAME TO` table rename moved UP as `acceptKeyword(KwTo)` (must precede the
fallthrough — TO is a RESERVED keyword `parseIdent` cannot consume) →
column-rename fallthrough `_ = p.acceptKeyword(KwColumn)` +
`parseIdent`/`expectKeyword(KwTo)`/`parseIdent` (mirrors the ALTER VIEW RENAME arm,
`ddl.go:7751-7777`). Parser-only — the existing `AlterTableRenameColumn` executor
already emits 42703 `column "…" does not exist`. Closes the 3 bare-form sites
(`rename test2 to testx`, `rename a to x`, `rename
"........pg.dropped.1........" to x`) — `expected keyword to (got …)` → 0 in the
diff; sites 2/3 now reach the executor and emit PG's 42703.

**Ninth slice landed (2026-08-16): `STORAGE` column clause** — the failing arm was
the CREATE TABLE column-definition `storage` clause (`col type STORAGE
{plain|external|extended|main}`), NOT the ALTER `SET STORAGE` arm (that one already
parses at ddl.go:8918-8930 and executes at operators_ddl.go:8573-8604).
`parseColumnConstraintList` had a `COMPRESSION` case but no `STORAGE` case, so
`storage` fell to the switch default → `expected ',' or ')'`. Added a `STORAGE`
case mirroring COMPRESSION (`acceptIdentKeyword("storage")` then one of the four
mode keywords) onto a new `ColumnDef.Storage` field (ast.go); `execCreateTable`
threads it onto `catalog.Column.Storage` on both the BodyOrder `addCol` path and
the fallback no-BodyOrder path, and a new `validateColumnStorage` enforces PG's
`GetAttributeStorage` datatype-vs-mode rule (tablecmds.c:22082-22112): a non-plain
mode on a plain-storage type (`typstorage=='p'`, e.g. int) raises 0A000 `column
data type integer can only have storage PLAIN` (type name via `pgFormatTypeName`,
so `int`/`int4` render "integer"). Closes the 2 `got storage` sites
(diff:2032/2066) → 0. Residuals ledgered: `has_toast_table` (TOAST-modeling,
`reltoastrelid` hardcoded 0) and the `STORAGE DEFAULT`/invalid-mode grammar forms.

**Tenth slice landed (2026-08-16): `NOT VALID`** — two sites, both byte-matching
PG. (a) CREATE TABLE table-level CHECK: both arms (`parseTableConstraintElement`
anonymous + the `CONSTRAINT name CHECK` twin) consume-and-drop a trailing
`NOT VALID` after `NO INHERIT` — PG auto-validates NOT VALID at CREATE TABLE
(`transformCheckConstraints` parse_utilcmd.c:2946 sets `skip_validation=true`,
`initially_valid=is_enforced`, `heap.c:2584-2587` writes `convalidated` from
`initially_valid`), so a fresh empty table's CHECK is created validated (no
`convalidated='f'`). (b) ALTER ADD [CONSTRAINT] NOT NULL: the arm consumes
`NOT VALID` order-independently (before OR after `NO INHERIT`, per gram.y
`ConstraintAttributeSpec` gram.y:6213-6252) onto the existing
`AlterTableAction.NotValid`, threaded through a new `notValid` param on
`AddNotNull` onto `NamedNotNullConstraint.NotValid`, written to pg_constraint
contype='n' convalidated `row[6]='f'`, and flipped back to 't' by a new
`NotNullConstraints` name-match loop in the VALIDATE CONSTRAINT arm (PG excludes
CONSTR_NOTNULL from the Phase-3 pre-scan, tablecmds.c:9956 — the flip is the whole
behavior). Closes `nv_parent` (diff:534) + `atnnparted` (diff:1075) → 0; `\d+`
renders `"dummy_constr" NOT NULL "id" NOT VALID`, then without the suffix after
VALIDATE. Residual ledgered: order-dependent CREATE TABLE trailer loop (a bare
`CHECK (...) NOT VALID` still fails).

**Eleventh slice landed (2026-08-16): `OF` / `NOT OF`** — two new executor
methods close the typed-table reassignment forms. Parser arms (ddl.go:9461-9481)
capture `OF type_name` → `AlterTableAddOf` (with `OfType`) and `NOT OF` →
`AlterTableDropOf` onto the new AST kinds (ast.go:3180-3189); `execAlterTableAddOf`
(operators_ddl.go:9387) resolves the composite type, rejects an inheritance parent
(42809 `typed tables cannot inherit`), then order-strictly zips the composite's
(compacted) fields against the table's non-dropped columns, emitting the four
42804 messages in PG's exact order (`table has column "%s" where type requires
"%s"`, `table "%s" has different type for column "%s"`, `table is missing column
"%s"`, `table has extra column "%s"`) — the type match derives the expected
canonical `catalog.Type` exactly as CREATE's `addCol` does, so `numeric(9,2)` vs
`numeric(8,2)` (typmod) and `bigint` vs `numeric` (base) both fail while `NOT
NULL` is ignored (PG: attnotnull need not match). Success stamps
`tbl.OfTypeOID = ct.OID`; `execAlterTableDropOf` (operators_ddl.go:9462) clears it
(42809 `"%s" is not a typed table` when it was never typed). Closes the 3
OF/NOT OF syntax-error sites + all 6 validation errors byte-exact; the `\d tt7`
reassign+`NOT OF` sequence renders x/y with `q` dropped. Residuals ledgered:
check_of_type 42809 parity (non-composite vs rowtype vs 42704), reloftype
restart-durability, and the missing table↔type dependency edge.

**Twelfth slice landed (2026-08-16): `SET WITHOUT OIDS`/`SET WITH OIDS` +
duplicate `[NOT] ENFORCED`** — the last two tiny C2 grammar sub-gaps, both
parser-only. (a) `SET WITHOUT OIDS`: the `SET WITHOUT CLUSTER` arm
(ddl.go:9332-9341) now also accepts `SET WITHOUT OIDS` and returns the existing
`AlterTableNoOp` — PG maps it to `AT_DropOids` whose exec is a silent no-op
(gram.y:2731-2738, tablecmds.c:5528-5530, `alter_table.out:1503` empty), so no
diagnostic is emitted. (b) `SET WITH OIDS`: a new guard before the `expected ADD
or DROP` fallthrough emits `syntax error at or near "WITH"` at the `with` token —
PG's gram.y has no `SET WITH` production, and scanner_yyerror echoes the raw
(uppercase) source token (scan.l:1234-1241); goopg's lexer lowercases keyword
Values, so the message re-uppercases the targeted token. (c) duplicate `[NOT]
ENFORCED`: a new `rejectDuplicateEnforced` helper (built on `isEnforcedAttr`,
which peeks bare `enforced` or `not`+`enforced` without false-positiving `NOT
NULL`/`NOT VALID`) emits a Raw `multiple ENFORCED/NOT ENFORCED clauses not
allowed` (PG `transformConstraintAttrs`, parse_utilcmd.c:3999-4027) after the
single-shot `[NOT] ENFORCED` consume at the 5 CHECK sites (inline + named column
CHECK, anonymous + named table CHECK, ALTER ADD CHECK) plus a `sawEnforced`
top-of-loop check inside `parseFKConstraintAttrs` (threaded through its 3 callers
via a new error return). Closes the SET WITHOUT OIDS / SET WITH OIDS /
ENFORCED-dup sites (diff → shared lines); the researcher's "ENFORCED dup is
C9-masked" was reclassified: the duplicate-ENFORCED error is a pure grammar gap,
distinct from the genuinely-C9 `only renameColumn add column x` block that
remains (sql:1205/1208).

Remaining C2 sub-gaps: ANALYZE tab(col) (4) — re-route: it is an ANALYZE/VACUUM
statement gap, not ALTER TABLE. C2 is otherwise complete; the 3-line
`only renameColumn add column x` / `column "x" already exists` divergence
(sql:1205/1208) is the C9 inheritance class, tracked separately.

**Thirteenth slice landed (2026-08-16): `ANALYZE tab(col)` / `VACUUM ANALYZE
tab(col)` column-list** — the final C2-adjacent gap, re-routed to the ANALYZE/VACUUM
statement parser. PG grammar: `AnalyzeStmt`/`VacuumStmt` →
`opt_vacuum_relation_list` → `vacuum_relation: relation_expr opt_name_list` with
`opt_name_list: '(' name_list ')'` (gram.y:11940/11952, :12021-12026,
:12016-12019) — each target carries its own `va_cols`. goopg's `parseObjectList`
(parser.go:2067) has no column-list arm, so `analyze atacc1(a)` stops at the `(`
and the trailing-token check emits `syntax error at or near "expected ';' or end
of input (got ()"`. All 4 alter_table sites (alter_table.sql:1056-1059) name
DROPPED columns of `atacc1`, so this case needs only the 42703 error path; the
valid-column stats *restriction* is deferred (ledger row) to vacuum.sql (M0134-0084).

Design — three coordinated changes in one slice (parser+AST+executor, so the
gate passes atomically):

1. **Parser** — new `parseVacuumTargets() ([]ObjectName, [][]string, error)`
   helper: each `parseObjectName()` then an optional `'(' parseIdent (','
   parseIdent)* ')'` column list, returning parallel `[]ObjectName` + `[][]string`
   (nil entry = no list). Do NOT modify `parseObjectList` (4 callers incl.
   ddl.go:2225/:6699). Wire into `parseVacuum` (parser.go:1900) and `parseAnalyze`
   (:2058); each column name uses `parseIdent`+`identText` (ColId semantics, the
   same reader `parseObjectName` uses).
2. **AST** — add `TargetCols [][]string` to `VacuumStmt` (ast.go:110) and
   `AnalyzeStmt` (ast.go:136), parallel to `Targets`.
3. **Executor** — new `resolveAnalyzeColumns(tbl *catalog.Table, cols []string,
   pos int) *ExecError`: a case-sensitive, dropped-skipping lookup reproducing PG
   `attnameAttNum` (parse_relation.c:3589-3609, `namestrcmp` + `!attisdropped` —
   NOT the case-insensitive `InMemory.LookupColumn`), returning 42703
   `column "%s" of relation "%s" does not exist` on the first unresolved name and
   42701 `column "%s" of relation "%s" appears more than once` on a repeat (dedup
   on the resolved column Ordinal, analyze.c:372-400). Call it from
   `expandAnalyzeTargets` (operators_analyze.go:189 loop, switch to `for i, name :=
   range o.stmt.Targets`) and from `expandVacuumTargets` (operators_vacuum.go:257
   loop, only when `vs.Analyze` is set — plain VACUUM ignores `va_cols`); both
   surface the error to their `Next`. The error message mirrors the existing
   `column %q of relation %q does not exist` at analyzer.go:733.

## C9 first slice — inherited column/constraint DDL guards (2026-08-16)

After C2 landed, the researcher reassessed the diff
(`tmp/ralph-handoffs/0134-0002-c3-reassess-research/report.md`): **4349-line diff,
104 hunks, 746 `+` / 849 `-`**. C9 inheritance is the largest remaining
correctness class (553 lines). This slice lands only the *guards* — the bounded,
low-risk portion — and defers InhCount bookkeeping to a follow-up.

**The defect.** goopg silently *mutates* inherited state where PG refuses. Three
ALTER arms read `col.Inherited` (bool, set at CREATE TABLE INHERITS / PARTITION OF /
ADD COLUMN-to-parent, operators_ddl.go:3226/4385) but never guard on it:

| goopg arm | today | PG oracle | fix |
|---|---|---|---|
| `execAlterDropColumn` (operators_ddl.go:21505) | silently rewrites the heap and deletes an inherited column | `ATExecDropColumn` tablecmds.c:9350 → 42P16 `cannot drop inherited column "%s"` | guard `col.Inherited` |
| RENAME COLUMN dispatcher arm (operators_ddl.go:8332) | renames without child propagation | `renameatt_internal` tablecmds.c:3916/3963 → `inherited column "%s" must be renamed in child tables too` / `cannot rename inherited column "%s"` | guard |
| `execAlterTableAddColumn` (operators_ddl.go:9637) | `ONLY`-with-children accepted | `ATExecAddColumn` tablecmds.c:7603 → `column must be added to child tables too` | guard |
| `execAlterTableDropConstraint` (operators_ddl.go:10239; InhCount guard at :10248 exists) | drops an inherited constraint | `ATExecDropConstraint` tablecmds.c:14106 → `cannot drop inherited constraint` | guard |
| inherited-constraint RENAME arm | renames | `rename_constraint_internal` tablecmds.c:4118 → `inherited constraint "%s" must be renamed in child tables too` | guard |

**Deferred (follow-up slice):** `Column.InhCount int` (multi-parent) for the
pg_attribute VALUES rows (hunks 1512/1525, `c1` attinhcount 2 vs 1) and the
depth0/merge NOTICEs. The bool guard passes multi-inheritance but the catalog rows
stay wrong until the int count lands.

**Landed (2026-08-16).** Diff 4349→4298 (−51), zero new divergence. The five guards
reproduce the oracle message + SQLSTATE byte-exact (42P16 throughout, `Pos: 0` — PG
emits no errposition on these refusals). The `ONLY` guards key off
`hasInheritanceChildren` (INHERITS ∪ PARTITION children, pg_inherits), which matches
PG's `expected_parents == 0 && find_inheritance_children(...) != NIL` exactly; the
inherited-column/constraint guards key off `col.Inherited`/`nc.InhCount > 0` with a
`colStillInherited`/`parentStillHasColumn` live-hierarchy narrowing so a stale flag
(after parent-side DROP or NO INHERIT) does not false-fire, and the NO INHERIT arm
clears the child's `Inherited` flag (PG decrements attinhcount). A parser change
records `AlterTableStmt.Only` (was accepted-and-discarded).

The projected 100-120 close was 51: the residual is pre-existing and un-closable by
the guards — `Column.InhCount int` multi-parent bookkeeping (attinhcount 1-vs-2),
LIKE+ATTACH-PARTITION `Inherited`, INHERIT child-validation, and INHERITS merge
NOTICEs, each ledgered (3 rows).

## C3 first slice — constraint row-validation scans (ADD CHECK / SET NOT NULL / VALIDATE CHECK)

C3 is "constraint validation scans absent": four ALTER arms register a constraint
without scanning existing rows, so goopg silently accepts data PG refuses. A
research pass (`tmp/ralph-handoffs/0134-0002-c3-constraint-scan-research/report.md`)
confirmed the exact PG behavior and split C3 into **two slices**:

- **Slice 1 (this section): the three non-index scans** — ADD CHECK, SET NOT NULL,
  VALIDATE CHECK. Each mirrors the existing FK scan twin
  `validateFKConstraintExistingRows` (operators_ddl.go:10548) — a page-Pin loop over
  live rows — factored into one shared `forEachLiveRow` helper.
- **Slice 2 (deferred, ledger row): the index-build path** — the ADD PK/UNIQUE
  23505 `DETAIL: Key (...) is duplicated.` (needs per-entry value capture in
  `btree.BulkEntry`, which today carries only `Key []byte + Ptr`), the ADD-PK-over-
  NULL 23502 scan, and `Pos=0` on the 23505 error (goopg emits a spurious `LINE 1`
  caret PG does not). Separately, the VALIDATE-FK anchor (sql:378) stays masked by
  a C4-class ADD-FK duplicate-name shadowing artifact (not this slice).

PG scan mechanics (the brief's `setRelNotNull`/`validateCheckConstraint` names do
not exist in PG 18.3 — the scans run in `ATRewriteTable`, tablecmds.c:6125, the
phase-3 work queue):

- **ADD CHECK** (`ATAddCheckNNConstraint` tablecmds.c:9911 → `ATRewriteTable`
  :6492-6498): when `!skip_validation` (grammar sets it for `NOT VALID`/`NOT
  ENFORCED`), per row `ExecCheck(con->qualstate)`; on FALSE → 23514 `check
  constraint "%s" of relation "%s" is violated by some row`. SQL 3-valued logic:
  only a definite FALSE violates; NULL/UNKNOWN passes. goopg gate:
  `!act.NotValid && !act.CheckNotEnforced`.
- **SET NOT NULL** (`ATExecSetNotNull` tablecmds.c:7913 → `set_attnotnull` →
  `ATRewriteTable` :6450-6463): first NULL row → 23502 `column "%s" of relation "%s"
  contains null values` (single message, aborts at first NULL). Distinct from the
  INSERT/UPDATE runtime message `null value in column %q ... violates not-null
  constraint` (operators_fk.go:1831) — do not conflate.
- **VALIDATE CHECK** (`ATExecValidateConstraint` tablecmds.c:12908 →
  `QueueCheckConstraintValidation` :13116 re-reads conbin): same 23514 scan; on
  success flips `convalidated` 'f'→'t'. NOT NULL VALIDATE already correct (C2 slice
  10); FK VALIDATE already wired (:7757).

Row-scan helper: there is no shared primitive today — the identical page-Pin loop
is duplicated in `validateFKConstraintExistingRows` (:10557-10596) and
`collectBTreeEntries` (:11013). Slice 1 adds `forEachLiveRow(tbl, func(*catalog.Row)
error) error` as a copy of that loop and uses it for the three new scans; the
existing FK/btree callers are NOT refactored onto it this slice (minimize blast
radius on the already-working FK scan). CHECK evaluation uses
`planner.ResolveIndexPredicate(expr, tbl)` once (planner.go:74) then
`evalExpr(planned, row, o.ctx)` per row — NOT the per-row mini-query rebuild in
`checkConstraints` (O(N·parse+plan)). All three scan errors set `Pos=0` (PG emits
no errposition on these refusals).

## C3 slice 2 — the index-build path (ADD PK/UNIQUE duplicate + ADD-PK-over-NULL)

A research pass (`tmp/ralph-handoffs/0134-0002-c3-slice2-research/report.md`)
corrected the slice-1 "deferred" note: **duplicate detection already exists**, it
is just mis-formatted (missing DETAIL + spurious `LINE 1`), and the 23502
ADD-PK-over-NULL scan is the only genuinely-absent scan. Three deliverables:

1. **23505 DETAIL + `Pos=0` (two existing raises).** `collectBTreeEntries`
   already rejects duplicates at operators_ddl.go:11335-11340 (non-NULL, via
   `sortBuildEntriesFindDuplicate` pgindex_btree.go:493) and :11294-11309
   (NULLS-NOT-DISTINCT). Both raise `23505 "could not create unique index %q"`
   with no DETAIL and `Pos: pos` (a spurious `LINE 1`). PG emits the DETAIL from
   `comparetup_index_btree` (tuplesortvariants.c:1686-1693) via
   `BuildIndexValueDescription` (genam.c:178-276):
   `DETAIL: Key (a)=(2) is duplicated.` — cols = comma-space list, each value via
   the opclass type's output function, NULL rendered `null`; **no errposition**.
2. **23502 ADD-PK-over-NULL scan (new).** `execAlterTableAddPrimaryKey`
   (operators_ddl.go:10031) sets `col.NotNull=true` but never scans existing
   rows. PG's `ATRewriteTable` verify scan (tablecmds.c:6456-6462) raises
   `23502 column "%s" of relation "%s" contains null values` for the first NULL.
   Ordering: dup check (23505) runs BEFORE the null scan (index build is pass 3,
   verify is phase 2) — a dup-and-NULL table yields 23505, NULL-only yields 23502.
   ADD UNIQUE must NOT null-scan (NULLs are distinct by default).
3. **Sibling: REFRESH MATVIEW non-concurrent unique build** (operators_ddl.go
   :16245-16256) re-wraps the 23505 from `materializeView` into a fresh 23505,
   dropping the DETAIL and restoring `Pos: s.Pos()` — propagate `ee.Detail` and
   `Pos=0`.

**Mechanics (value capture).** After sort, `collectBTreeEntries` holds only
`[]btree.BulkEntry` (bulkload.go:58-61: `Key []byte + Ptr`), not the source row,
so the DETAIL must be captured at entry-construction time. Add a `KeyDesc string`
field to `BulkEntry` filled at :11320 with the rendered `Key (cols)=(vals)`
description (mirror `buildUniqueConstraintDetail`/`nndDetail`, operators_storage.go
:8606-8623/:7894-7912, suffix `is duplicated.`); change
`sortBuildEntriesFindDuplicate` to return the dup index (currently `bool`); render
`entries[i].KeyDesc + " is duplicated."` at :11338. The NND raise at :11306 renders
inline (its `row` is still in scope). The 23502 scan reuses slice-1's
`forEachLiveRow` (mirror the SET NOT NULL 23502 at :9322-9330).

**Deferred/known:** (a) `Datum.Format()` has no float kind — float4/float8 key
values would render empty; ledgered (the alter_table PK/UNIQUE keys are int/text).
(b) multi-column PK null reporting is attnum-order in PG vs declared-key-order in
the naive scan — only observable with 2+ NULL PK cols. (c) PG prints
`Duplicate keys exist.` when the user lacks SELECT on key cols — goopg has no ACL
check here.

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

## partition-key DROP COLUMN guard — structural walk (surfaced by C3, 2026-08-16)

**Bug (pre-existing, surfaced by the C3 row-scan running against partitioned
tables).** `execAlterDropColumn` (`operators_ddl.go:21970-21986`) decides whether
the dropped column is an expression partition key via
`strings.Contains(strings.ToLower(fmt.Sprintf("%v", expr)), colLower)` — a
string search over the Go `%v` rendering of the expression node. That rendering
embeds raw pointer addresses, so the hex digits can contain the target column
name and flip the guard silent↔error between runs (ASLR), with no code change.
The `ALTER TABLE partitioned DROP COLUMN b` regress section therefore oscillates.

**Fix.** Replace the `%v`-contains heuristic with a structural walker
`partitionKeyExprUsesColumn(e parser.Expr, colLower string) bool` that recurses
the expression tree and matches a `*parser.ColumnRef` by column name
(case-insensitive) — the same name-only convention as the plain-key loop
(`strings.EqualFold`, `evalPartitionKeyExpr` operators_storage.go:2773) and the
existing `funcExprContainsName` (operators_ddl_partition.go:1301), which this
walker mirrors. Node kinds to recurse: `ColumnRef` (leaf match), `FuncCall`
(args), `BinaryOp` (Left/Right), `UnaryOp` (Operand), `CastExpr` (Operand),
`CollateExpr` (Operand), plus the CaseExpr/ExtractExpr/IsNullExpr arms that
`funcExprContainsName` lacks (the partition-key validator `validatePartKeyExprInner`
operators_ddl_partition.go:237-327 accepts CaseExpr/ExtractExpr/IsNullExpr via
its default arm, so the walker must not silently miss them).

**SQLSTATE correction (same line).** The guard raises `0A000`
(`ERRCODE_FEATURE_NOT_SUPPORTED`) where PG raises `42P16`
(`ERRCODE_INVALID_TABLE_DEFINITION`) — `ATExecDropColumn` calls
`has_partition_attrs` (`tablecmds.c:9358`) which reports 42P16. Correct it while
here; message text already matches PG (`cannot drop column %s because it is part
of the partition key of relation %s`).

**Sibling (landed 2026-08-16).** `execAlterColumnType` (operators_ddl.go:22137)
has the same partition-key guard, raising 42P16 `cannot alter column %q because
it is part of the partition key of relation %q` before any rewrite — a bare key
column, or a column referenced inside an expression key (walked via
`partitionKeyExprUsesColumn`). One difference from the DROP COLUMN arm:
`ATExecAlterColumnType` carries `parser_errposition(pstate, def->location)`
(tablecmds.c:14450) where `ATExecDropColumn` (tablecmds.c:9358-9364) does not, so
the ALTER-TYPE raise uses `Pos: act.Pos()` — the column-name token location
threaded by `parseAlterColumnAction` → `colPos` (`internal/parser/ddl.go`) — and
the regress `LINE 1 … ^` caret points at the column name, byte-matching PG
(expected alter_table.out:3977-3983). For the same reason the ALTER-TYPE
coercion-failure 42804 arms (evaluation-time, no source location) use `Pos: 0`,
not `act.Pos()`.

## C9 residuals — ONLY-on-partitioned guard + descendant-partition recursion (2026-08-16)

After the own-key guards landed (DROP COLUMN + ALTER TYPE), two C9 residuals
remain, both in the partitioned-parent block (`alter_table.sql:2850-2858,2902-2903`),
and they cascade off one root cause: goopg's `ALTER TABLE ONLY <partitioned> DROP
COLUMN` silently succeeds where PG refuses, so `b` is dropped from `list_parted2`'s
catalog and every later statement in the block then reports a spurious 42703 on the
already-gone column. Three guards close the block (research
`0134-0002-c9-residuals-partition-recursion-research`):

1. **ONLY-on-partitioned DROP COLUMN guard** (ledger row 1416). PG
   `ATExecDropColumn` tablecmds.c:9385-9389 raises, when the target is itself
   `RELKIND_PARTITIONED_TABLE` and has ≥1 child and `!recurse` (ONLY): 42P16
   `cannot drop column from only the partitioned table when partitions exist` +
   HINT `Do not specify the ONLY keyword.`, `Pos: 0`. goopg must raise the same
   in `execAlterDropColumn` after the own-key guard. `ATExecAlterColumnType` has
   **no** analogous ONLY guard (its recursion is prep-time and silently skipped
   when `!recurse`, tablecmds.c:14568), so ALTER TYPE does not get one.
2. **Descendant-partition recursion** (ledger row 1419). PG recurses into
   descendant partitions and re-runs the partition-key guard on each descendant's
   OWN key, reporting the DESCENDANT's relation name: `ALTER TABLE list_parted2
   DROP COLUMN b` → 42P16 `cannot drop column "b" because it is part of the
   partition key of relation "part_5"` (`ATExecDropColumn` one-level recursion
   tablecmds.c:9373/9422-9424; `ATPrepAlterColumnType` prep-time `find_all_inheritors`
   :14576). goopg checks only the parent's own key and reports 42703. goopg already
   has the walker — `allDescendants(im, tbl, 0)` (operators_fk.go:949, BFS over
   `InheritanceChildren ∪ PartitionChildren`, transitive, excludes the parent) —
   and the predicate — `partitionKeyExprUsesColumn` (operators_ddl_partition.go:1338).
   Walk descendants when `!only`, fire on the FIRST partitioned descendant whose key
   uses the column. DROP COLUMN uses `Pos: 0`; ALTER TYPE uses `Pos: act.Pos()` (the
   original column-name errposition, out:4581-4582). Sort the descendant list by OID
   for deterministic message text (PG BFS-sorts siblings by OID, pg_inherits.c:200-201).
3. **ALTER TYPE inherited-column guard** (new residual, unmasked once (1) lands).
   `ALTER TABLE part_2 ALTER COLUMN b TYPE text` must raise 42P16 `cannot alter
   inherited column "b"` + errposition (tablecmds.c:14436-14440) — the DROP
   (`:22052`) and RENAME (`:8462`/`:8496`) arms already have the twin; ALTER TYPE
   lacks it. Same `col.Inherited && colStillInherited` gate, `Pos: act.Pos()`.

`only` is not yet threaded into either function: `execAlterTable` (operators_ddl.go
:9544/:9548) holds `s.Only` in scope but calls `execAlterDropColumn(tbl, act)` /
`execAlterColumnType(tbl, act)`. Add an `only bool` param at both call sites.

**Landed (2026-08-16).** All three guards + a shared `partitionKeyUsesColumn`
helper (operators_ddl_partition.go) + the `allDescendants` `visited` set. Diff
4110→4102 (−8 lines): the three guard statements (`:2850` ONLY, `:2902` DROP,
`:2903` ALTER TYPE) are byte-green. Remaining in the block (NOT closed — separate
C9 residuals, ledgered): `part_2 ADD COLUMN c text` (no `cannot add column to a
partition` guard, tablecmds.c:7250) and the `part_2 DROP/RENAME/ALTER` inherited
refusals, all blocked by the pre-existing ATTACH-PARTITION `Inherited`-marking gap
(ledger row 1410 (a)) — plus the cyclic-ATTACH 42P17 gap the `visited` set guards
against rather than fixes.

## C9 final — partition-child DDL refusals + circular ATTACH (2026-08-17)

The four residuals left by the previous slice close as one bundle, because S2 is
the root cause the other three sit on: goopg's `ALTER TABLE … ATTACH PARTITION`
never marked the child's columns inherited, so every inherited-DDL refusal was
latent on an ATTACHed child (the regress `part_2`).

1. **S1 — ADD COLUMN on a partition.** `ATExecAddColumn` refuses with
   ERRCODE_WRONG_OBJECT_TYPE **42809** `cannot add column to a partition` (no
   detail/hint, `Pos: 0`) as its FIRST check, gated on `relispartition &&
   !recursing` — before the system-column collision check and before the
   IF NOT EXISTS notice (`postgres/src/backend/commands/tablecmds.c:7247-7250`).
   goopg's guard is therefore also first in `execAlterTableAddColumn`, and gates
   on `im.IsPartitionChild(tbl.OID)` — **not** `Table.PartitionParentOID`, which
   is populated only by `CREATE TABLE … PARTITION OF` and stays 0 for an ATTACHed
   child.
2. **S2 — `Inherited` marking at ATTACH, cleared at DETACH.** PG's
   `MergeAttributesIntoExisting` (`tablecmds.c:17500`), reached via
   `CreateInheritance`, bumps `attinhcount` and clears `attislocal` on the child's
   same-named columns; `RemoveInheritance` (`:18009-18014`) reverses it on DETACH.
   goopg mirrors this with `markAttachedColumnsInherited` /
   `clearAttachedColumnsInherited`. **Both ATTACH arms** must call the marker —
   the immediate arm and `ApplyPendingPartitionAttaches` (the COMMIT-deferred
   arm) — and **both DETACH arms** (plain and CONCURRENTLY-finalize) must call
   the clearer; the DETACH side is required for `part_3_4`'s attislocal
   assertions (`alter_table.out:4416-4422`) to stay byte-green.
3. **S3 — `colStillInherited` live-map fallback.** The guard predicate consulted
   `tbl.PartitionParentOID` only, so it stayed false on an ATTACHed child even
   after S2. It now falls back to `im.PartitionParentOf(tbl.OID)` when the field
   is 0; the field branch still wins when set, so PARTITION-OF behaviour is
   unchanged.
4. **S4 — circular ATTACH.** `ATExecAttachPartition` tests the prospective parent
   for membership in `find_all_inheritors(attachrel)` and raises
   **42P07 ERRCODE_DUPLICATE_TABLE** `circular inheritance not allowed` with
   errdetail `"%s" is already a child of "%s".` — **parent named first, child
   second**, no hint, `Pos: 0` (`tablecmds.c:20338-20362`). The 42P17 written in
   the previous slice's design note and ledger row was a placeholder and is not a
   real SQLSTATE. goopg reuses `allDescendants(im, childTbl, 0)` (BFS over
   `InheritanceChildren ∪ PartitionChildren`, so ATTACH edges count) plus the
   self-attach case; the parent's own descendants are not walked. The `visited`
   set from the previous slice is retained as defence in depth.

**Unmasked by S4: `DROP CONSTRAINT` ignored ONLY.** With the cycle rejected, the
regress reached `ALTER TABLE ONLY list_parted2 DROP CONSTRAINT check_b`, which
goopg cascaded to children. PG's `dropconstraint_internal` with `recurse=false`
removes the constraint from the parent only and leaves each child's inherited
copy (`tablecmds.c:14025-14110`, oracle `alter_table.out:4541-4542`), so
`execAlterTableDropConstraint` now takes an `only bool` (threaded from `s.Only`)
and skips the child cascade when set.

**Landed (2026-08-17).** Diff 4102→4073 (−29 lines); `:2848-2858` and the
cyclic-ATTACH statements are byte-green. Tests:
`operators_ddl_c9_residuals_final_test.go` (S1/S2/S4 + DROP-CONSTRAINT-ONLY) and
the extended `TestAlterTableDescendantWalkCycleSafe`. Still open in this block
(ledgered 2026-08-17): ADD CONSTRAINT duplicate-name merge accounting (double
`check_b` ⇒ extra `merging constraint` NOTICEs), the already-a-partition 42809
re-ATTACH guard (`alter_table.sql:2697`), the ONLY-guards for SET NOT NULL /
ADD CONSTRAINT (ledger row 1423), and the pre-existing constraint-deparse
rendering gap (`((b <> 'zz'))` vs `(b <> 'zz'::bpchar)`).

## C4 — ADD FOREIGN KEY validation semantics (2026-08-16)

**Class: FK semantics (`tablecmds.c`, correctness).** The ADD-FK executor arm
(`case parser.AlterTableAddForeignKey`, `operators_ddl.go:7665-7718`) appends to
`tbl.ForeignKeys` with **no duplicate-name guard, no column-existence validation,
and no existing-row scan** — only a referenced-table-existence 42P01 check
(`:7690`). The regress FK block (alter_table.sql:355-383) therefore diverges four
ways before the C4 anchor:

- `:355 foreign key(c) …` — PG 42703 `column "c" referenced in foreign key
  constraint does not exist`; goopg silently appends.
- `:358 references attmp2(b) …` — PG 42703 (ref column `b`); goopg appends.
- `:361` valid columns, dangling row `(5,50)` — PG 23503; goopg appends.
- `:367` (valid, post-DELETE) — PG succeeds; goopg appends a *fourth* same-name
  entry.

Those four same-name `attmpconstr` entries pile up in `tbl.ForeignKeys` (goopg
never rejects the adds, PG rejects all three failing ones so the name stays
free), so the later `VALIDATE CONSTRAINT attmpconstr` (`:372`) breaks on the first
(stale, `NotValid=false`) match and skips the scan — masking out:499-500's 23503.
The statement-time DROP (`:368`) removes only the first matching entry
(`DropForeignKeyConstraint`, catalog.go:20769), so it cannot clear the pile-up.

**Fix — make the ADD arm PG-faithful, in PG's exact order** (all `Pos: 0` unless
noted):

1. **42710 duplicate-name guard** — when the ADD carries an explicit
   `CONSTRAINT name`, reject if that name already exists on the table across all
   constraint kinds (FK + CHECK + PK/UNIQUE/EXCLUDE index + NOT NULL — the same
   enumeration `execAlterTableDropConstraint` already walks). PG raises this only
   for an explicit `conname` (`ATExecAddConstraint` CONSTR_FOREIGN,
   tablecmds.c:9824-9833 → `ConstraintNameIsUsed`, pg_constraint.c:412;
   auto-named constraints skip the check via `ChooseConstraintName`). Message
   byte-exact: `constraint "%s" for relation "%s" already exists`
   (ERRCODE_DUPLICATE_OBJECT, conname + relation name).
2. **42703 source-column check** — for each `act.Columns[i]`, a case-sensitive
   match over `tbl.Columns` (`c.Name == col`, skipping dropped) — NOT
   `InMemory.LookupColumn` (case-insensitive). `transformColumnNameList`
   tablecmds.c:13327-13346 via `SearchSysCacheAttName` (case-sensitive).
   Message `column "%s" referenced in foreign key constraint does not exist`
   (ERRCODE_UNDEFINED_COLUMN).
3. **42703 ref-column check** — same loop over `act.RefColumns` against the
   referenced table; skip when `len(act.RefColumns)==0` (PG infers the PK in that
   case — `transformFkeyGetPrimaryKey` tablecmds.c:13382 — and goopg's scan
   already infers via `pkColumns`). `ATAddForeignKeyConstraint` resolves source
   cols (tablecmds.c:10166) before ref cols (:10192/10205), so source 42703
   precedes ref 42703.
4. **23503 existing-row scan** — when `!act.NotValid`, after building `fk`, call
   the existing `validateFKConstraintExistingRows(tbl, fk)` (operators_ddl.go:10775,
   landed C3 slice 1) and propagate its error unchanged (it already emits the
   byte-exact `insert or update on table … violates foreign key constraint …` +
   `Key (…) is not present in table …` with `Pos: 0`, via `assertParentExists`
   operators_fk.go:608). PG `validateForeignKeyConstraint` tablecmds.c:13694.
5. **Pos suppression on VALIDATE FK 23503** — the VALIDATE arm currently wraps the
   23503 with `ee.Pos = act.Pos()` (`operators_ddl.go:7757-7761`), but PG's
   FK-violation ereports carry no errposition (`ri_triggers.c` `ri_ReportViolation`
   :2778 has no `errposition`), so the expected `.out` has no `LINE 1:` caret.
   Drop that wrap so the anchor is byte-exact.

**Deferred (ledger):** FK type-compatibility 42804 (`transformFkeyCheckAttrs` →
`findFkeyCast` tablecmds.c:10435), 42830 `there is no unique constraint matching
given keys` (`transformFkeyCheckAttrs` tablecmds.c:13657 — goopg does not verify
the referenced table has a unique index on the ref columns), system-column
0A000, and the FK column-count (42908) checks.

## C10 — ALTER TYPE assignment-coercibility gate (2026-08-16) — LANDED

**Refined scope (researcher `0134-0002-c10-alter-type-coercion-research`):** the
C10 data-loss crash is ALREADY fixed — C2 slice 5 (`fec178bd`) captures the
per-row `evalCast` error and returns it before Phase-3 truncation, so `internal
error: expected int, got kind 1` + empty table are NOT reachable at HEAD. The
remaining genuine divergence is a **single pair**: `ALTER COLUMN atcol1 TYPE
boolean` on an `int8` column (`alter_table.sql:1356`) silently succeeds where PG
raises 42804. Every other C10-region diff line is a cascade of that one flip.

**Root cause.** goopg's per-row `evalCast` (`internal/executor/expr.go:3467`) has
an int→bool arm (`NewBoolDatum(d.Int != 0)` at :3484) that is correct for the
EXPLICIT cast context (`1::boolean` is legal) but wrong for the ASSIGNMENT
context. PG gates coercion by `CoercionContext` per call site: `ATExecAlterColumnType`
calls `coerce_to_target_type(..., COERCION_ASSIGNMENT, ...)` (tablecmds.c:14503),
and `find_coercion_pathway` (parse_coerce.c:3152) returns NONE for int→bool —
int4→bool is castcontext `'e'` (explicit-only, pg_cast.dat:90-92), int8→bool has
no pg_cast row at all, and the I/O fallback (:3273) requires a string-category
target. So PG raises 42804 at tablecmds.c:14499-14517 with **no errposition**.

**Fix (static gate, not a global evalCast change).** `evalCast` is shared by
EXPLICIT paths (`1::boolean`) and the ALTER-TYPE ASSIGNMENT path, so a global
strictness change would break the explicit cast. Instead, add a static
assignment-coercibility predicate and call it at the TOP of `execAlterColumnType`
(`internal/executor/operators_ddl.go`, before the `nBlocks==0` early return —
PG's 42804 fires even on an empty table, where the per-row evalCast hook never
runs):

- `canAssignCast(src, dst catalog.Type) bool` — returns false for the pairs PG's
  COERCION_ASSIGNMENT rejects that goopg currently accepts (int2/int4/int8 → bool,
  text → int2/int4/int8), true otherwise (preserving current per-row evalCast
  behaviour, which already handles int8→int4 narrowing byte-identically).
- On false, raise the existing 42804 arm (byte-exact message + HINT, Pos 0 —
  the arms at operators_ddl.go:22374-22384 already match tablecmds.c:14495-14511).

Deferred (ledger): the FULL assignment-coercion matrix (bool→int, and any other
permissive pair not exercised by alter_table.sql) and the INSERT/UPDATE
assignment-coercion sibling — both out of C10's diff scope; see the ledger row.

**Landed (2026-08-16, this loop):** `canAssignCast` + `noUsingCoercionError`
added beside `execAlterColumnType` (`operators_ddl.go`); the gate fires on the
no-USING path after the name-unchanged no-op, before the `Pool == nil` / `nBlocks
== 0` early returns. The per-row no-USING 42804 raise was refactored onto the
shared helper (bytes unchanged); `evalCast` and the WITH-USING arm untouched.
Diff 4113 → 4110 (−3), confined to the `anothertab` C10 region — sql:1356 now
byte-matches PG. Verified against real PG 18.3 (explicit `1::boolean` preserved,
empty-table and non-empty int8→bool both 42804, int8→int4 narrowing preserves
value). 6 subtests in `operators_ddl_alter_type_coercion_test.go`.

## C11 decomposition + C11a view-relkind guard (2026-08-17)

Research (`researcher`, 2026-08-17) established that the C11 row in the class
table above **conflates three unrelated problems** under one `view_dml.go` cell.
Two corrections to that row, first:

- `internal/executor/view_dml.go` **does not exist**. Auto-updatable view DML
  lives in `internal/optimizer/view_dml.go`; CREATE/ALTER VIEW DDL lives in
  `internal/executor/operators_ddl.go`; the `pg_get_viewdef`/`\d+` display path
  is `internal/executor/expr.go:8242-8292`. Three files, three mechanisms.
- "`CREATE OR REPLACE VIEW` not propagating to dependents" is **not the bug**.
  Dependent column counts already track correctly (the M0134 slice-1
  `viewColumnMap` crash fix). What diverges is the "View definition:" SQL *text*:
  `execCreateView` (`operators_ddl.go:5244`) stores `vt.ViewDef = s.RawDef`
  verbatim and `pg_get_viewdef` echoes that string. PG freezes `SELECT *` into an
  explicit target list at CREATE VIEW time (`view.c` `DefineVirtualRelation`) and
  re-derives the text from the frozen tree (`ruleutils.c` `get_query_def`).
  goopg has neither half — this is the "top-level-`*` freeze", and it is a
  **missing SQL deparser**, not a propagation gap. `catalog.Table.Columns` IS
  correctly frozen via `planSchema`, so `\d+`'s column table already matches PG;
  only the definition text below it is wrong.

C11 therefore splits into three independently-scoped items:

| id | problem | diff lines | verdict |
|----|---------|-----------|---------|
| C11a | ALTER TABLE structural actions accepted on a view | 6 | **LANDED 2026-08-17** |
| C11b | `to_json` (and the JSON-producing builtin family) missing | 1 + cascade | deferred — needs a composite/row→JSON encoder design |
| C11c | no SQL deparser for `pg_get_viewdef`/`\d+` view text | 55 | deferred — cross-cutting, own milestone |

### C11a — LANDED (2026-08-17)

`alter_table.sql:1162-1166` (`alter table myview alter column test {drop,set}
not null`) and `:1519-1520` (`alter table myview drop d`) silently SUCCEEDED:
`execAlterTable` had **no relkind guard for views at all**. PG raises 42809
`ALTER action %s cannot be performed on relation "%s"` with
`DETAIL: This operation is not supported for views.` — message from
`ATSimplePermissions` (`tablecmds.c:6739`), detail from
`errdetail_relkind_not_supported` (`pg_class.c:24-37`), action text from
`alter_table_type_to_string` (`tablecmds.c:6596`).

Implementation: two new helpers beside `execAlterTable` —
`viewAllowedAlterAction(kind)` (explicit **allow-list**, refuse by default) and
`alterActionName(kind)` (PG's action strings, covering all 43
`AlterTableActionKind` values the dispatch handles so the guard never reaches a
generic fallback) — plus a pre-scan of **all** `s.Actions` placed before the
per-action loop. The pre-scan mirrors `ATPrepCmd`, which runs
`ATSimplePermissions` for every subcommand before Phase 2 executes any of them,
so a multi-action `ALTER TABLE <view> a, b` fails atomically rather than
half-applying.

Allow-set, each case source-verified against the `AT_*` switch at
`tablecmds.c:4943-5282` (NOT taken from a summary): RENAME table/column/
constraint (`renameatt()`/`rename_constraint()` do their own relkind-agnostic
checks and never route through `ATSimplePermissions`), ALTER COLUMN SET/DROP
DEFAULT (`AT_ColumnDefault`, ATT_VIEW-allowed so INSERT-into-view can have
default-ish behavior), reloptions SET/RESET (incl. `security_barrier` /
`check_option`), and goopg's internal `AlterTableNoOp`. Two classifications that
a category-level reading would have gotten wrong:

- `SET STATISTICS` is ATT_MATVIEW|ATT_INDEX but **not** ATT_VIEW
  (`tablecmds.c:5041-5044`) — refused.
- ENABLE/DISABLE/ENABLE ALWAYS/ENABLE REPLICA **RULE** is
  `ATT_TABLE | ATT_PARTITIONED_TABLE` only (`tablecmds.c:5245-5256`) — refused
  on views despite rules being the view mechanism itself.

`Pos` must be **0**, not the action's position: `ATSimplePermissions` /
`errdetail_relkind_not_supported` never call `errposition()`, so PG's expected
output carries no `LINE 1:` cursor. A first pass that used `act.Pos()` emitted a
spurious cursor and only shrank the diff to 4058; with `Pos: 0` it reaches 4048.

Materialized views are unaffected: `tbl.IsMatView` is a distinct relkind with a
different allow-set (ATT_MATVIEW) and `execCreateMatView` never populates
`tbl.View`, so the `tbl.View != nil` gate is matview-exclusive by construction.

Diff **4073 → 4048 (−25)**. `create_view` (2505) and `updatable_views` (4156)
verified byte-identical to a stash-based pre-change baseline — no over-refusal.

## C7 slice 1 — an inline column CHECK must keep its user-given name (2026-08-18)

The formatter-tail scoping pass (researcher `af1679ca6e2065b46`,
`tmp/ralph-handoffs/m0134-0002-s01-formatter-tail/report.md`) re-measured the
case at HEAD `f8284ae2`: **4048 lines / 107 hunks**, matching this doc's last
recorded number exactly — no drift. Its main structural finding is that the
remaining diff is **no longer dominated by C7/C12/C13/C14**. Newly-visible
classes sitting outside the original 14-class frame: ownership/ACL checks are
absent entirely (`must be owner of …` is never raised), `pg_locks` is always
empty, an EXPLAIN Append/**constraint-exclusion planner** gap (a planner gap,
not C14 "verbosity"), and a `\d+` describe drift (missing `Compression` column
and `Access method: heap` line; the Index `Definition` column renders the whole
`CREATE INDEX` statement instead of just the expression). Those are future
classes, deliberately NOT folded into C7.

The one genuinely-C7, genuinely-isolated slice it found is implemented here.

**Divergence.** `CREATE TABLE t (a int CONSTRAINT con1 CHECK (a > 0))` parsed
the name `con1` and discarded it; `execCreateTable` then unconditionally
auto-named the constraint `t_a_check`. A later `ALTER TABLE t RENAME CONSTRAINT
con1 TO …` therefore failed with `constraint "con1" … does not exist` — the
symptom that made this look like a *rename* gap when it is a *naming* gap.

**PG oracle.** `parse_utilcmd.c` `transformCheckConstraints` preserves
`Constraint->conname` verbatim; `heap.c` `ChooseConstraintName` generates
`<rel>_<col>_check` **only when `conname` is NULL**. Naming is decided at parse
time, not at catalog time.

**Fix.** The sibling pattern already shipped for inline UNIQUE and inline NOT
NULL was the whole design: `ColumnDef` already carried `UniqueConstraintName`
and `NotNullConstraintName`, and CHECK was the missing third.
`internal/parser/ast.go` gains `ColumnDef.CheckConstraintName`;
`internal/parser/ddl.go`'s `CONSTRAINT <name> CHECK (…)` column arm stores the
identifier it was already parsing and throwing away; `execCreateTable`
(`internal/executor/operators_ddl.go`) prefers that name and falls back to the
existing auto-name when empty. Table-level named CHECK (`s.TableNamedChecks`)
was already correct and is untouched.

**Sibling-path audit (Hard-won Rule #2).** Three other `ColumnDef`-CHECK
consumers were checked. `CREATE TABLE … LIKE INCLUDING CONSTRAINTS` copies from
the materialized source table's catalog constraints, so it inherits the fix with
no code change. `PARTITION OF` uses a separate `PartitionCheckConstraint`
structure, not `ColumnDef` — unaffected. `ALTER TABLE … ADD COLUMN` never reads
`col.CheckExpr` **at all** (`operators_ddl.go:10033`) and silently drops an
inline CHECK regardless of naming — a pre-existing missing feature, ledgered
rather than fixed here.

**Result.** Diff **4048 → 4039 (−9)**; three `RENAME CONSTRAINT con1 TO con1foo`
statements now succeed. Hunk count rose 107 → 108, which is benign: the newly
matching content splits one large divergent hunk into two smaller ones (verified
by diffing the before/after `.diff` files). No new divergence. Guards:
`internal/parser` `TestParseCheckNotEnforced/CreateTableInlineColumnNamed`
(plus a new `…/CreateTableInlineColumnAnonymousNameEmpty` sibling pinning the
anonymous form) and `internal/executor`
`TestColumnLevelCheckPreservesExplicitName`; both confirmed FAIL-pre/PASS-post.

## C19 — LANDED (2026-08-18): the `\d+` drift was half harness, half `pg_get_indexdef` arity

**The premise was inverted.** The previous loop recorded goopg as *missing* the
`Compression` column and the `Access method: heap` footer. It is the opposite:
goopg **over-produces** them. Upstream `pg_regress` invokes every psql with
`-v HIDE_TABLEAM=on -v HIDE_TOAST_COMPRESSION=on`
(`postgres/src/test/regress/pg_regress_main.c:74-79`), so the expected `.out`
files were generated with those sections suppressed; goopg's own runner
(`scripts/pg-regress-runner.sh`) never passed them. Because they are
*client-side* psql variables consumed by `describe.c`, this was a **harness**
bug with zero engine content, and the fix corrects the comparison for the entire
regress suite rather than for `alter_table` alone. Lesson worth generalising: a
`\d`-shaped divergence must be checked against `pg_regress`'s own psql
invocation before it is attributed to the engine.

**The genuine engine half** was `pg_get_indexdef` arity. psql's `\d+` reaches
the per-column form (`describe.c:1936-1937`), and PG's
`pg_get_indexdef_worker` (`ruleutils.c:1270`) switches to an `attrsOnly` mode
via `pg_get_indexdef_ext` (`ruleutils.c:1198-1217`) whenever `colno != 0`,
emitting **only** that column's name or parenthesised key expression — the
COLLATE / opclass / ASC-DESC / NULLS decorations are gated off wholesale at
`ruleutils.c:1459`. goopg's implementation discarded both the `colno` and
`pretty` arguments and always returned the full `CREATE INDEX` statement, so
every `\d+` index `Definition` cell diverged.

**Fix.** `internal/catalog/catalog.go` gains `BuildIndexDefColumn(idx, colno)`
beside the existing `BuildIndexDef`, walking key columns then INCLUDE columns on
one 1-based combined ordinal (the same convention `PGIndexRowsForDBOid`'s
`indkey` construction already uses). `internal/executor/expr.go`'s
`pg_get_indexdef` case parses `Args[1]`: `colno == 0`/absent keeps the existing
full-statement path, `colno != 0` routes to the new helper. The `pretty`
argument stays a documented no-op — goopg has no deparser to pretty-print
against, which is the already-ledgered C11c gap.

**Oracle correction found while implementing.** An out-of-range `colno` on a
*valid* index OID returns the **empty string**, not NULL —
`pg_get_indexdef_worker` never range-checks `colno`, its loop simply never
matches; NULL is reserved for an OID that does not resolve. Verified directly
against a throwaway real PG 18.3 instance
(`pg_get_indexdef('t19_idx'::regclass, 3, true) IS NULL` → `f`,
`pg_get_indexdef(0, 1, true) IS NULL` → `t`). The implementation follows the
oracle, not the brief.

**Sibling-path audit (Hard-won Rule #2).** All 18 hits for
`pg_get_indexdef|buildIndexDefString|BuildIndexDef` resolve to the single
canonical chain (`expr.go` case → `buildIndexDefString` →
`catalog.BuildIndexDef`), plus comments, `colno=0`-only unit tests and `pg_proc`
seed rows. There is no second index-definition renderer and no separate
`\d`-support path, so there was no twin to change.

**Result.** `alter_table` **4039 → 3981** lines after the harness half, **→ 3968**
after the engine half (−71 total). `create_table` 831 → 791 confirms the
suite-wide harness yield; `create_index` 3613 → 3613 confirms the harness change
is a no-op where it does not apply. Hunk count rose 108 → 111, the same benign
`diff --unified=5` windowing artifact seen in C7 slice 1: removing the
Compression/access-method noise deleted the lines that used to bridge two
genuinely-different regions, so one hunk now renders as two. Proof point —
`atnnpart1_pkey`'s `Definition` went from
`CREATE UNIQUE INDEX atnnpart1_pkey ON public.atnnpart1 USING btree (id)` to
exactly `id`, byte-identical to PG, contributing zero diff lines, which is
precisely why that hunk split. Guard:
`internal/executor/pg_get_indexdef_test.go`
(`TestBuildIndexDefColumn` / `TestBuildIndexDefColumnExpression`), confirmed
FAIL-pre (compile error) / PASS-post.

## PG oracle citations

- `postgres/src/test/regress/pg_regress_main.c:74-79` — the psql variables
  (`HIDE_TABLEAM`, `HIDE_TOAST_COMPRESSION`) upstream sets on every regress
  invocation; goopg's runner must match them or `\d+` output over-produces.
- `postgres/src/backend/utils/adt/ruleutils.c:1270` `pg_get_indexdef_worker`
  (+ `pg_get_indexdef_ext` :1198-1217, decoration gate :1459) — per-column
  `attrsOnly` rendering when `colno != 0`; psql caller
  `postgres/src/bin/psql/describe.c:1936-1937`.
- `postgres/src/backend/parser/parse_coerce.c` — `coerce_to_target_type` (:78),
  `can_coerce_type` (:557), `find_coercion_pathway` (:3152), I/O fallback (:3273).
- `postgres/src/include/catalog/pg_cast.dat` — castcontext rows (int8→int4 'a'
  :23, int4→bool 'e' :90-92).
- `postgres/src/backend/catalog/partition.c:255` — `has_partition_attrs(Relation,
  Bitmapset *, bool *used_in_expr)`: plain key via `bms_is_member`, expression key
  via `pull_varattnos(expr, 1, &expr_attrs)` (`optimizer/util/var.c:296`) +
  `bms_overlap`. Call sites tablecmds.c:9358 (DROP COLUMN) / :14443 (ALTER TYPE).
- `postgres/src/backend/commands/tablecmds.c` — ALTER TABLE grammar + execution
  (`ATExecAddConstraint`, `ATExecAlterColumnType`, constraint naming).
- `postgres/src/backend/commands/explain.c:1619-1633` (`ExplainNode` indent),
  `:4774` (`ExplainSubPlans`).
- `postgres/src/backend/utils/adt/arrayfuncs.c` — `array_cat`/`||`.
- `postgres/src/backend/rewrite/rewriteHandler.c` — view DML rules.
