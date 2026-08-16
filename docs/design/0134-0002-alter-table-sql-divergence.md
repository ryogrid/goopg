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

## PG oracle citations

- `postgres/src/backend/commands/tablecmds.c` — ALTER TABLE grammar + execution
  (`ATExecAddConstraint`, `ATExecAlterColumnType`, constraint naming).
- `postgres/src/backend/commands/explain.c:1619-1633` (`ExplainNode` indent),
  `:4774` (`ExplainSubPlans`).
- `postgres/src/backend/utils/adt/arrayfuncs.c` — `array_cat`/`||`.
- `postgres/src/backend/rewrite/rewriteHandler.c` — view DML rules.
