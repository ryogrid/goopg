# 0122-0002 — `pg_relation_size` family returns real storage sizes

Status: accepted, landed (`f0b2bdb3`). Source: `unimplemented_feat.json` entry
97 (M0122-0002 cluster, "Catalog system functions & pg_* view stubs"),
`.ralph/fix_plan.md` M0122-0002.

**Cluster closure (2026-07-04, loop #7):** this doc's title covers only the
`pg_relation_size` slice; the M0122-0002 fix_plan item bundles ~9 catalog
function "quick wins" total. Re-verified the rest against current HEAD:
`isfinite`, `justify_hours`/`justify_days`/`justify_interval`,
`pg_get_expr`, `pg_get_serial_sequence` are all genuinely implemented (not
stubs) in `internal/executor/expr.go`; `pg_get_indexdef` is implemented and
under active, separately-tracked extension via the M0119-0004 DU-002 slices
(not duplicated here, per the fix_plan's own M0122/M0119-0004 dedup rule).
`regexp_matches` was the one real gap — its case in `evalExpr` unconditionally
returned `NullDatum`. See `docs/design/README.md`'s `0122-0002` row and
`.ralph/deferral_ledger.md` (2026-07-04, M0122-0002 row) for the fix
(`regexpFirstMatchArray` in `internal/executor/expr.go`, merged into the
`regexp_match` case arm) and its documented residual (no SRF/multi-row `'g'`
flag support — `internal/executor/regexp_match_test.go` covers the
now-correct scalar/first-match path). M0122-0002 is closed as of this loop.

## Problem

`pg_relation_size`, `pg_total_relation_size`, `pg_indexes_size`, and
`pg_table_size` (`internal/executor/expr.go`) were hardcoded stubs from
M0097-0018: every call returned a fixed `8 * 1024` (8 kB) regardless of the
target relation's actual on-disk footprint. Any query or tool that reads
these to observe real growth (monitoring dashboards, `\dt+`/`\di+`-style
size columns, capacity-planning scripts) got a constant that never reflected
inserts, deletes, or index growth — silently wrong, not merely approximate.

## Fix

New helpers in `internal/executor/expr.go` compute real sizes from the
storage manager's block counts, mirroring PostgreSQL's `dbsize.c`
(`calculate_relation_size` / `calculate_table_size` /
`calculate_total_relation_size`):

- `resolveRegclassOID` — evaluates the first argument (already a numeric OID
  once a `regclass` cast has run, per the existing `::regclass` cast
  behavior in this same file) to a `uint32` OID. Shared pattern already used
  by `pg_get_indexdef`/`pg_get_statisticsobjdef`.
- `relationFileNodeForOID` — resolves an OID to its `storage.RelFileNode`,
  covering both ordinary tables (`catalog.InMemory.LookupTableByOID` +
  `RelFileNode`) and indexes (`LookupIndexByOID` + `IndexRelFileNode`) —
  `pg_relation_size` in real PG accepts either.
- `relationForkSize` — the byte size of one fork (`main`/`fsm`/`vm`/`init`)
  of a relation. **Must** check `storage.Pool.Exists` before calling
  `NBlocks`: `NBlocks` on a fork that was never created would silently
  create it empty (smgr `O_CREATE` semantics — the same gotcha already
  documented for VACUUM/prune paths), which a read-only size query must
  never do as a side effect. goopg declares `storage.FSMFork` /
  `storage.VisibilityMapFork` (`internal/storage/page.go`) but no code path
  anywhere in the engine ever creates those fork files — `grep` confirms
  zero non-declaration references — so `fsm`/`vm` always resolve to `0`
  via the `Exists` check; this is an accurate "never materialized" answer,
  not a second stub.
- `relationAllForksSize` — sums main+fsm+vm for one relation (what
  `pg_relation_size` without children means for `pg_table_size`/
  `pg_indexes_size` purposes).
- `evalPgRelationSize(relation [, fork])` — one relation, one fork (default
  `main`); an unrecognized fork name raises `22023` (matches PG's
  `forkname_to_number` rejecting an invalid fork argument).
- `evalPgTableSize(relation)` — the table's own forks plus its TOAST
  relation's forks (`catalog.InMemory.ToastRelFileNode`), **not** its
  indexes — matches PG's own table/index size split. goopg does not model a
  separate TOAST *index* relfilenode (confirmed: no
  `toastIndexRelFileNode`-shaped accessor exists anywhere in
  `internal/catalog`), so there is nothing to add on that front — this is
  the current architecture, not a shortcut taken by this fix.
- `evalPgIndexesSize(relation)` — sums `relationAllForksSize` over every
  `catalog.Index` whose `Table.OID` matches.
- `evalPgTotalRelationSize(relation)` — `pg_table_size + pg_indexes_size`.

All four cases in the `evalExpr` switch (`internal/executor/expr.go`,
"Size functions" section) now dispatch to these instead of returning the
fixed `8 * 1024`.

## Tests

`internal/executor/pg_relation_size_test.go`:

- `TestPgRelationSizeReflectsActualStorage` — creates a table, inserts 200
  rows, adds an index, and checks: `pg_relation_size` is a positive multiple
  of 8192 (not the old fixed 8192 constant coincidentally matching — it is
  asserted to scale, see below); `pg_indexes_size` likewise; `pg_table_size
  == pg_relation_size` (no TOAST relation here); `pg_total_relation_size ==
  pg_table_size + pg_indexes_size`; an explicit `'fsm'` fork read is exactly
  `0` (the never-created-fork path, not an error); an invalid fork name
  raises an error.
- `TestPgTableSizeIncludesToastRelation` — inserts a 1 MiB value (forces
  TOAST) and asserts `pg_table_size > pg_relation_size`, proving the TOAST
  relation's bytes are actually being added rather than the two functions
  coincidentally returning the same stub value.

## Gates

- `go build ./...` — clean.
- `go vet ./internal/executor/...` — clean.
- `go test ./internal/executor/...` — full package PASS.
- pgbench smoke via the pre-commit hook — PASS.
- `scripts/tpch-spotcheck.sh` not run: this change touches only four scalar
  catalog-introspection functions never referenced by the TPC-H query set,
  and does not touch the planner, codec, or any row-producing operator.

## Follow-up: `regexp_matches` SRF `'g'`-flag multi-row expansion (2026-07-04, later loop)

The residual noted above — `regexp_matches`' real SRF semantics ("with the
`'g'` flag, one row per match") — now lands for the SELECT-list/target-list
position, the same position `generate_series`/`unnest` are already wired for:

- `internal/planner/plan.go` gains `RegexpMatchesCol` (`ColIdx`, `StringExpr`,
  `PatternExpr`, `FlagsExpr`) and `ProjectSet.RegexpMatchesCols`.
- `internal/planner/planner.go`'s `buildSelectSrfProjectSet` detects a bare
  `regexp_matches(string, pattern[, flags])` target exactly like it already
  detects `unnest(...)`, resolves its args, and assigns the output column
  type `text[]` (regexp_matches always returns an array, unlike
  `generate_series`/`unnest`'s type-dependent output).
- `internal/executor/operators_project_set.go`'s `projectSetOp.openSelectSrfMode`
  gains a `regexpMatchesResults` branch that calls a new
  `evalRegexpMatchesSRF` (`internal/executor/expr.go`), zipped into the output
  exactly like `unnestResults`/`userResults` already are.
- `internal/executor/expr.go` factors the pre-existing
  `regexpFirstMatchArray` into a shared `regexpMatchArrayDatum` (one match's
  array literal) plus a new `regexpAllMatchesArrays(re, s, global bool)`:
  without `global` it still returns at most one match (mirrors the scalar
  case), with `global` it returns one `Datum` per match via
  `FindAllStringSubmatchIndex`.

Unlike `unnest`'s "flatten one array's elements one-per-row" shape, each
`regexp_matches` step's *value* is already a whole match's capture-group
array — so `openSelectSrfMode` doesn't flatten anything further; it just
zips one already-built array `Datum` per step, the same way
`userSrfCols`/`unnestCols` results are zipped.

**Verified against a real PostgreSQL 18.3 cluster** (not just derived from
reading `postgres/src/backend/utils/adt/regexp.c`), byte-for-byte:

```
SELECT regexp_matches('foo bar baz', '\w+', 'g');   →  {foo} / {bar} / {baz}  (3 rows)
SELECT regexp_matches('foo bar baz', '\w+');         →  {foo}                 (1 row)
SELECT regexp_matches('---', '[0-9]+', 'g');         →  (0 rows)
SELECT regexp_matches('2026-07-04 2027-01-02',
       '([0-9]+)-([0-9]+)-([0-9]+)', 'g');           →  {2026,07,04} / {2027,01,02}  (2 rows)
```

The zero-row case is notable: a `regexp_matches` SRF call with no match
yields **zero rows** (matches PG), which is different from the pre-existing
scalar-position fallback (still used whenever `regexp_matches` is NOT a bare
SELECT-list target, e.g. nested in a larger expression) that returns SQL
`NULL` for no match — that asymmetry is real PG behavior, not a goopg bug
(the scalar and SRF positions are genuinely different PG code paths).

Tests: `internal/executor/regexp_matches_srf_test.go`
(`TestRegexpMatchesSRF` — bare-target cases above; `TestRegexpMatchesSRFPerRow`
— per-child-row zipping against a passthrough column over a real table, one
source row with no match contributing zero output rows).

Gates: `go build ./...`/`go vet ./...` clean; `go test
./internal/executor/... ./internal/planner/...` PASS (no regressions);
`scripts/tpch-spotcheck.sh` run (Q12/Q13 spot-check) since this touches the
shared `ProjectSet`/`buildSelectSrfProjectSet` planner path — see
`.ralph/working_set.md` for the result recorded this loop.

**Still deferred:** the FROM-clause form (`SELECT * FROM
regexp_matches(...)`) is not wired — that needs a `planFromRegexpMatches`
counterpart to `planFromUnnest`/`planFromGenerateSeries`
(`internal/planner/planner.go`), analogous to how `unnest`/`generate_series`
each have both a target-list path (this doc) and a FROM-clause path. Ledger
row appended (`.ralph/deferral_ledger.md`, M0122-0002).

## Deferred / out of scope

- The `fork` argument's `fsm`/`vm`/`init` cases are wired through correctly
  but are moot until some future milestone actually materializes those
  forks as on-disk files — at that point `relationForkSize`'s `Exists`
  gate + fork-tagged `RelFileNode` already do the right thing with no
  further change needed here.
- `pg_relation_size` on a *sequence* OID currently falls through
  `relationFileNodeForOID`'s two lookups (table, index) and returns `NULL`
  — sequences are not tables in goopg's catalog model. Real PG returns the
  sequence's own tiny on-disk size. Not fixed here: no ledger row filed
  because this was already the pre-existing scope of `unimplemented_feat.json`
  entry 97 (which only named the four function stubs, not per-relkind
  coverage), and no existing test or tool in this codebase depends on
  `pg_relation_size(sequence)`.

## Cross-references

- `.ralph/fix_plan.md` M0122-0002 ("Catalog system functions & pg_* view
  stubs").
- `unimplemented_feat.json` entry 97 — not yet flipped to a `resolved`
  status field in this loop: `.ralph/fix_plan.md` M0122-0001 (the triage
  task that introduces that field across all 181 entries) had not run yet,
  and no entry in the file currently has a `status` key — adding one
  ad hoc to a single entry ahead of that batch pass would invent schema the
  triage task owns. Recorded here instead so the M0122-0001 triage can cite
  this doc + commit `f0b2bdb3` as proof entry 97 is done.
- `docs/design/README.md` index entry for this doc is **not yet added**:
  at commit time the file was concurrently dirty from another in-flight
  Ralph loop's uncommitted edit (`root-0026` follow-up work); adding a line
  risked a lost-update race against that edit. See `.ralph/working_set.md`
  for the pending-reconciliation note.
