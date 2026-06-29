# Expression extended-statistics round-trip in pg_dump (DU-002 slice 316)

- Milestone/spec: M0119-0004 (pg_dump 002–010 / DU-002 catalog-view parity battery)
- Status: accepted
- Oracle: `postgres/src/backend/utils/adt/ruleutils.c`
  `pg_get_statisticsobj_worker`; `postgres/src/bin/pg_dump/pg_dump.c`
  `getExtendedStatistics` / `dumpStatisticsExt`.

## Problem

Slice 314 made a simple-column `CREATE STATISTICS` object dumpable end-to-end
(parser → `catalog.StatisticsObject` → `pg_get_statisticsobjdef(oid)` →
`pg_dump`). It explicitly left **expression** extended statistics
(`CREATE STATISTICS s ON (a + b) FROM t`) unhandled: the parser only flagged
`HasExpr` and skipped the expression tokens, the catalog stored nothing for it,
and `BuildStatisticsObjDef` returned `""` (declined) whenever `HasExpr` was set.
`pg_dump`'s `dumpStatisticsExt` emits `pg_get_statisticsobjdef(oid)` verbatim, so
a declined object was **silently dropped from the dump** — a restore would lose
the statistics object entirely.

## Oracle behavior

`pg_get_statisticsobj_worker` (ruleutils.c:1654) deparses the ON list as:

- **all simple columns first** (in `stxkeys` order), **then all expressions**,
  regardless of their original ON-list order; a single running `colno` drives
  comma separation across both lists.
- each expression is deparsed with `PRETTYFLAG_PAREN`; the result is emitted
  bare when `looks_like_function(expr)` (a top-level function call) and otherwise
  wrapped in parentheses: `appendStringInfo(&buf, "(%s)", str)`.
- `ncolumns = stxkeys.dim1 + list_length(exprs)`. The kinds clause is emitted
  only when some kind is disabled **and** `ncolumns > 1` — a single-target object
  must be expression statistics, where PG omits the clause.

`getExtendedStatistics` selects only `oid, stxname, stxnamespace, stxowner,
stxrelid, stxstattarget` — it does **not** read `stxkeys`/`stxexprs`. The DDL
comes solely from `pg_get_statisticsobjdef(oid)`, so goopg only needs that
builtin to reconstruct the object; the `pg_statistic_ext` virtual view is
unchanged.

## Change

End-to-end capture + deparse of expression targets:

- **Parser** (`internal/parser/ddl.go`, `ast.go`): `CreateStatisticsStmt` gains
  `Exprs []Expr`. The ON-list loop now parses a non-simple-column target with
  `p.parseExpr()` (PG's grammar parenthesizes expression elements, so the leading
  token is normally `(`, which `parsePrimary` reduces to the bare inner
  expression). `HasExpr` is still set; on a parse error the loop restores
  `p.idx` and falls back to the original tolerant skip, leaving `Exprs` empty.
- **Executor** (`internal/executor/operators_ddl.go` `execCreateStatistics`):
  deparses each `Expr` via `defaultExprToSQL`, which already fully parenthesizes
  binary ops (`a + b` → `(a + b)`) and leaves a bare function call unwrapped
  (`lower(a)` → `lower(a)`) — exactly the two cases `pg_get_statisticsobj_worker`
  distinguishes. The rendered strings pass to `RegisterStatisticsFull`.
- **Catalog** (`internal/catalog/catalog.go`): `StatisticsObject.Exprs []string`
  (pre-rendered final form); `RegisterStatisticsFull` gains an `exprs` param.
  `BuildStatisticsObjDef` now (a) declines only when `HasExpr && len(Exprs)==0`
  (expression present but uncaptured); (b) computes `ncolumns =
  len(Columns)+len(Exprs)`; (c) emits columns first then expressions with a
  shared `colno` for comma separation.

No `pg_statistic_ext` view change (pg_dump never reads `stxkeys`/`stxexprs` for
the DDL). Dump-fidelity only — goopg neither computes nor consumes extended
statistics. Blast radius nil: new fields default nil; the decline path is
preserved for genuinely unreconstructable expressions; TPC-H/pgbench carry no
statistics objects.

## Verification

- **DU-002 slice 316** in `TestPort_PgDumpConnectionSetup`: fixtures
  `statext_expr` (`ON (a + b)` → single target, kinds clause suppressed) and
  `statext_mix` (`ON a, (b + c)` → column-first, then parenthesized expression);
  both asserted byte-identical vs **real pg_dump 18.3** (4.6 s) PASS.
- Units `TestBuildStatisticsObjDef` extended: single-expr (default + disabled
  kind both suppress the clause), col+expr ordering, single-kind col+expr
  (clause re-emitted), bare-function-expr (unwrapped), uncaptured-decline.
- `internal/parser` + `internal/catalog` + `internal/executor` suites PASS;
  `go build ./...` clean; pgbench TPC-B smoke = pre-commit hook.

## Still open under M0119-0004

`ALTER STATISTICS … SET STATISTICS n` (`stxstattarget`; goopg has no ALTER
STATISTICS); the broader pg_dump 002–010 catalog-view parity battery (further
slices); extended-protocol commit-time deferral (architecturally entangled —
the extended protocol is auto-commit-per-statement).
