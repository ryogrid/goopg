# 0119-0004 — CREATE STATISTICS round-trip in pg_dump (DU-002 slice 314)

Status: accepted
Milestone: M0119-0004 (deferral-ledger backlog — pg_dump 002–010 / DU-002 catalog parity)

## Problem

`pg_dump` reproduces every extended-statistics object (`CREATE STATISTICS`) in the
`SECTION_POST_DATA` schema dump. Its `dumpStatisticsExt`
(`src/bin/pg_dump/pg_dump.c:18286`) runs

```sql
SELECT pg_catalog.pg_get_statisticsobjdef('<oid>'::pg_catalog.oid)
```

and emits the result verbatim (plus a trailing `;`). The candidate objects come
from `getExtendedStatistics`, which scans `pg_statistic_ext` for
`stxname, stxnamespace, stxowner, stxrelid, stxstattarget`.

goopg already exposed a `pg_statistic_ext` virtual table populated from
`StatisticsObject` (so the `getExtendedStatistics` scan returned rows), **but**:

- `parseCreateStatisticsTail` captured only the object name and the `FROM` table —
  it *skipped* both the optional `(kinds)` clause and the entire `ON` column list.
- `catalog.StatisticsObject` had no kinds/columns fields.
- `pg_get_statisticsobjdef(oid)` was unimplemented (returned NULL).

So `pg_dump` got a NULL definition and the statistics object was silently dropped
from the dump.

## PostgreSQL reference

`pg_get_statisticsobj_worker` (`src/backend/utils/adt/ruleutils.c:1652`) builds:

```
CREATE STATISTICS <nsp>.<name> [(kinds)] ON <col>, ... FROM <nsp>.<rel>
```

Key rules mirrored here:

- The **kinds clause** (`ndistinct`, `dependencies`, `mcv`, in that order) is
  emitted *only* when not all three are enabled **and** the object spans more than
  one column. A default `CREATE STATISTICS s ON a, b FROM t` enables all three, so
  no clause appears; `CREATE STATISTICS s (ndistinct) ON a, b FROM t` emits
  `(ndistinct)`. `STATS_EXT_EXPRESSIONS` is ignored (built automatically).
- Columns are simple `pg_attribute` references rendered via `quote_identifier`.
- The `FROM` relation is schema-qualified (pg_dump runs with an empty
  `search_path`, so `generate_relation_name` always qualifies).

## Change

Threaded the kinds + columns from parse time through to the dump path:

- **`parser` (`ast.go`, `ddl.go`)**: `CreateStatisticsStmt` gains `Kinds`,
  `Columns`, `HasExpr`. `parseCreateStatisticsTail` now captures the `(kinds)`
  idents (lowercased) and the `ON` list of simple column names; an expression
  target (anything that is not a bare ident, e.g. `(b + c)` or `f(a)`) sets
  `HasExpr` and is skipped to the next comma / `FROM`. The `IF NOT EXISTS` probe
  was corrected to use the `KwIf`/`KwNot`/`KwExists` keyword tokens (the prior
  `acceptIdentKeyword("if")` never matched because `IF` lexes as a keyword — a
  latent bug now covered by a parser test).
- **`catalog` (`catalog.go`)**: `StatisticsObject` gains `Kinds`, `Columns`,
  `HasExpr`; new `RegisterStatisticsFull` carries them (`RegisterStatistics` kept
  as a thin wrapper). New `StatisticsByOID` and `BuildStatisticsObjDef` — the
  latter reproduces `pg_get_statisticsobj_worker` for the simple-column case
  (reusing `quoteCollationIdent`, a `quote_identifier` mirror), resolving the
  `FROM` relation via `LookupTableByOID`. Returns `""` for an expression-bearing
  object (declined — not reconstructable by this path).
- **`executor` (`operators_ddl.go`, `expr.go`)**: `execCreateStatistics` calls
  `RegisterStatisticsFull`; new `pg_get_statisticsobjdef(oid)` builtin resolves the
  object via `StatisticsByOID` and returns `BuildStatisticsObjDef`.

## Scope / limitations

- **Expression statistics** (`CREATE STATISTICS s ON (a + b) FROM t`) are not
  reconstructed: the parser flags `HasExpr`, the dump path declines (NULL), and the
  object is omitted from the dump — same shape as before for that case, but now
  explicit. Reconstructing expressions needs a deparser for the stored AST and is
  a follow-up slice.
- Dump-fidelity only: goopg does not compute or use extended statistics; this is
  purely the catalog/round-trip surface pg_dump reads.
- Blast radius nil for everything else: the new catalog fields default
  nil/false, `pg_get_statisticsobjdef` is a fresh builtin, and TPC-H/pgbench carry
  no statistics objects.

## Gates

- New **DU-002 slice 314** in `TestPort_PgDumpConnectionSetup`
  (`statext_all` → default kinds, no clause; `statext_nd (ndistinct)` → explicit
  single-kind clause) PASS vs real pg_dump 18.3 (4.7s).
- New units `TestBuildStatisticsObjDef` (catalog: default/single/two/all kinds +
  expr-declined) + `TestParseCreateStatistics` (parser: default/explicit kinds,
  `IF NOT EXISTS`, expression target) PASS.
- `internal/parser` + `internal/catalog` + `internal/executor` suites PASS;
  `go build ./...` clean; pgbench smoke = pre-commit hook.

## Still open under M0119-0004

Expression extended statistics (deparse); pg_dump 002–010 catalog parity (further
slices); extended-protocol commit-time deferral.
