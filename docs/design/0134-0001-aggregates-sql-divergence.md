# M0134-0001 — `aggregates.sql` divergence map + harness prerequisite

**Status:** accepted
**Date:** 2026-08-15
**Task:** M0134-0001 (`.ralph/fix_plan.md`) — make
`postgres/src/test/regress/sql/aggregates.sql` match vanilla PG 18.3, then flip
its CSV row to `pass` / `pass_required=yes`.
**Gate:** `scripts/pg-regress-runner.sh aggregates` (normalised `diff -U5` →
`tmp/regress-diffs/aggregates.diff`).

## SQL surface

`aggregates.sql` (1637 lines) exercises the aggregate engine: `avg`/`sum`/`count`/
`min`/`max` over `int2`/`float4`/`numeric`, `stddev`/`var` (pop + samp), `regr_*`/
`covar_*`/`corr`, `bool_and`/`bool_or`, `bit_and`/`bit_or`, `string_agg`/`array_agg`,
`json*_agg`, ordered-set and `FILTER`/`DISTINCT`/`GROUP BY` variants, and a handful of
error cases. It depends on two things the runner does not provide:

1. **`test_setup.sql` data** — `onek`/`tenk1`/`aggtest`/`person`/`int8_tbl`/… are
   populated via `COPY … FROM :'filename'`, where `filename` is built from the
   psql variable `abs_srcdir` imported by `\getenv abs_srcdir PG_ABS_SRCDIR`.
2. **`create_aggregate.sql`** — the user-defined aggregates (`newavg`, `my_avg`,
   `my_sum`, …) it references are created there, not in `aggregates.sql`.

## Divergence map (fresh re-run @ HEAD 2026-08-15 — 2759-line diff; P1 harness-fixed → 2204, P4 harness-fixed → 1935, P3a harness-fixed → 1786)

| # | share | pattern | class |
|---|-------|---------|-------|
| P1 | 64.2% | every base table empty ⇒ `avg`/`count`/group-by return NULL/0/`(0 rows)` | **harness** — `PG_ABS_SRCDIR` never exported, so `\getenv abs_srcdir` leaves it unset and every `COPY … FROM :'filename'` fails |
| P2 | 20.4% | `EXPLAIN` text diverges: `(stats)`, `HashAggregate (N keys)`, `*planner.GenerateSeries`, missing min/max→InitPlan+IndexOnlyScan + GROUP-BY functional-dep removal + presorted aggregation | engine, broad |
| P3 | 7.5% | `psql:file:line:` prefix (harness: `-f` vs stdin redirect — **RESOLVED 2026-08-15**, 56→0 lines) + missing `LINE n:`/`^` position context in parse-analysis errors (engine — **open**, 27 lines) | mixed |
| P4 | 5.2% | `function … does not exist` for user-defined aggregates | **harness** — `create_aggregate.sql` prerequisite never run — **RESOLVED 2026-08-15** (runner runs it best-effort) |
| P5 | 1.3% | `string_agg` over `bytea` drops the delimiter (text `','` fails the `KindBytes` gate) | ~~engine, narrow~~ **non-issue** — the bytea section already matches PG (verified: `grep bytea tmp/regress-diffs/aggregates.diff` → empty) |
| P6 | 1.1% | `pg_get_viewdef` deparse lowercase/unqualified | engine |
| P7 | 0.2% | collation propagation (`"POSIX"` → `default`) | engine |
| P8 | <1% | `column ref f1/0 on nil slot` (partial-index DDL), `outer column ref s1/level=1 out of range` (lateral+agg), missing GROUP-BY-via-USING error | engine |

## Root causes (top two)

- **P1 (harness):** `scripts/pg-regress-runner.sh` builds the `PSQL` array
  (`psql … --no-psqlrc -X -a -q`) without exporting `PG_ABS_SRCDIR` (nor
  `PG_LIBDIR`/`PG_DLSUFFIX`). pg_regress sets it in the environment
  (`postgres/src/test/regress/pg_regress.c:734
  setenv("PG_ABS_SRCDIR", inputdir, 1)`), which `test_setup.sql:6` /
  `aggregates.sql:6` import via `\getenv abs_srcdir PG_ABS_SRCDIR`, then build
  `\set filename :abs_srcdir '/data/agg.data'` and `COPY … FROM :'filename'`.
  With the var unset the COPY path is empty/garbage and every table stays empty.
  The setup step additionally strips the `\getenv`/`\set ` lines
  (`grep -vE '^\s*\\(getenv|set\s|setenv)'`), which deletes the `\set filename`
  lines too. **These are standard psql meta-commands** (the strip's comment is
  wrong) — the correct fix is to export the env vars and stop stripping them.
- **P5 (was "engine", reclassified NON-ISSUE 2026-08-15):** the code at
  `internal/executor/operators_join_agg.go:2713-2724` does gate the `bytea`
  `string_agg` delimiter on `dv.Kind == KindBytes` (a text `','` would leave
  `delimHex` empty), but the actual `aggregates.sql` bytea delimiters are `''`,
  `NULL`, and `decode('ee','hex')` (all handled), and the one text-`','`-with-bytea
  case (`aggregates.sql:859`) already matches. `grep bytea tmp/regress-diffs/aggregates.diff`
  is empty — **no live divergence**. The code path is a latent fidelity gap
  (PG casts the delimiter to bytea regardless, `postgres/src/backend/utils/adt/varlena.c:507
  bytea_string_agg_transfn`) but is not exercised by this case; do not spend a
  loop on it.

## Decomposition (slice plan)

1. **Harness prerequisite (LANDED 2026-08-15, commit 82f3de2e)** — fix `scripts/pg-regress-runner.sh` to
   export `PG_ABS_SRCDIR`/`PG_LIBDIR`/`PG_DLSUFFIX` and stop stripping the
   `\getenv`/`\set` lines in the setup step, so `test_setup.sql` and
   `aggregates.sql`'s own `COPY … FROM` populate the tables. Removes P1 (64%).
2. **P4 `create_aggregate.sql` dependency (LANDED 2026-08-15)** — run
   `create_aggregate.sql` best-effort after `test_setup.sql` in the runner's
   setup phase. Self-contained (defines its own `sum3`/`aggf_trans`/`least_accum`/
   `cleast_accum` inline; built-in transitions for the rest). Removes P4 (5.2%).
3. **P3 error-position context** — **(a) harness half LANDED 2026-08-15:** drop the
   `psql:file:line:` prefix by feeding each `.sql` via stdin redirect (`< file`)
   instead of `-f`, mirroring `pg_regress_main.c:74-75` (56 prefix lines → 0).
   **(b) engine half LANDED 2026-08-15** — emit the `P` field for planner errors on
   both protocol paths + re-point 7 raise sites to the offending subexpression;
   raw-verify 23/23 common carets byte-match PG. Cross-case note:
   `docs/design/0134-0001-p3b-error-position-context.md`. Two deferrals remain
   (22P02 type-coercion location; byte-vs-char multibyte index).
4. **P2 `EXPLAIN` format** — broadest remaining engine gap; separate milestone-sized effort
   (mapped + decomposed 2026-08-15 → `docs/design/0134-0001-p2-explain-format.md`,
   slices S1–S10; note the diff-direction correction `-`=PG/`+`=goopg).
5. **P6/P7/P8** — deparse, collation, residual expr errors.
6. ~~P5 bytea string_agg delimiter~~ — reclassified non-issue (see map above).

After slices 1–3a the case stands at 1786 diff lines (P3 engine half + P2 are the
long poles; P6/P7/P8 are tail).

## PG oracle citations

- `postgres/src/test/regress/pg_regress.c:734` — `setenv("PG_ABS_SRCDIR", inputdir, 1)`.
- `postgres/src/test/regress/sql/test_setup.sql:6-8,137-211` — `\getenv`/`\set`/`COPY` data load.
- `postgres/src/backend/utils/adt/varlena.c:507` — `bytea_string_agg_transfn`.
- `postgres/src/test/regress/pg_regress_main.c:74` — stdin-redirect error prefix (P3 harness half).
