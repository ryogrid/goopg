# M0134-0182 — `type_sanity.sql`: RESERV date/time literals landed, PARKED

Status: **PARKED** (case sized live for the first time; one real, verified
fix landed — PG's `now`/`today`/`tomorrow`/`yesterday`/`epoch` date/time
input keywords, previously entirely unimplemented — plus a newly-unmasked
`int2vector` materialization bug and several REFACTOR-tier buckets ledgered
for later milestones).

## What the file tests

`postgres/src/test/regress/sql/type_sanity.sql` (587 lines) is a pure
catalog-consistency check: every query asserts `(0 rows)` by construction
(`SELECT ... FROM pg_type t1 WHERE <bad-shape predicate>`), scanning
`pg_type`/`pg_class`/`pg_attribute`/`pg_range` for internally-inconsistent
rows. A single `CREATE TABLE tab_core_types AS SELECT <one literal of every
core type>` populates a fixture the tail of the file cross-checks against
`pg_type` to make sure no core type was missed.

## Sizing (this loop, 2026-09-01)

`scripts/pg-regress-runner.sh -v type_sanity`: **0/1 PASS, 1151 diff lines,
9 `^+ERROR`** (first live run — CSV was `not-tried`).

### Root-cause bucketing (confirmed via a live throwaway server, not diff
inspection alone — `SELECT count(*) FROM pg_proc` etc. against a fresh
`./bin/goopg init` cluster)

1. **`pg_proc`/`regproc` exposes only ~32 hand-curated builtins at runtime,
   not the full PG18 catalog — REFACTOR-tier, dominant remaining bucket.**
   `'array_subscript_handler'::regproc`, `'array_in'::regproc`,
   `'array_recv'::regproc`, `'range_typanalyze'::regproc`,
   `'array_typanalyze'::regproc`, `'raw_array_subscript_handler'::regproc`
   all raise `function "X" does not exist` (7 of the 9 `^+ERROR`s). Traced
   live: `internal/initdb/pg_proc_seed_data.go`'s `pgProcAllEntries()` (3397
   rows, `cmd/gen-pg-proc-data`-generated) IS written to the on-disk heap at
   initdb time (`bootstrapPgProcTuples`, confirmed via `wc -c` on a fresh
   `base/*/1255` — 778 KB, far larger than 32 rows would produce), but
   goopg's OWN query engine never reads that heap file for `pg_proc` — a
   fresh cluster's `SELECT count(*) FROM pg_proc` returns exactly **32**
   (matching `catalog.builtinProcsByName` in `internal/catalog/catalog.go`,
   a small hand-curated map built for the DU-002 pg_dump/op_class fixtures)
   plus any user-created routines (`CREATE FUNCTION myf(...)`
   immediately shows up; `int4pl`/`array_in`/every other PG builtin does
   not). `reg_identifier.go`'s regproc-cast arm falls back to the SAME
   `catalog.LookupBuiltinProc` map after missing the live `Routines()`
   registry, so the gap is consistent end-to-end: the 3397-row pg_proc.dat
   mirror is **write-only**, present on disk purely for a PG standby doing a
   raw heap scan, and invisible to every one of goopg's own SQL paths
   (`SELECT * FROM pg_proc`, `'name'::regproc`, `'name'::regprocedure`).
   Fixing this for real means making the live query engine's `pg_proc` heap
   scan (or an equivalent runtime index) actually consume
   `pgProcInitialEntries()`'s full seed set instead of the 32-entry curated
   map — a genuine REFACTOR (visibility/heap-scan wiring, not a data gap),
   out of scope for this loop. This is very likely the same class of gap
   behind other regress cases' assorted "function does not exist" noise for
   otherwise-common PG builtins; a dedicated milestone should own it.
2. **RESERV date/time input keywords were entirely unimplemented — FIXED
   this loop.** `'today'::date` (used inside `tab_core_types`'s `CREATE
   TABLE ... AS SELECT`) raised `22007: invalid input syntax for type date:
   "today"`, which meant `tab_core_types` was never created, cascading into
   the file's tail `relation "tab_core_types" does not exist` — 2 of the 9
   `^+ERROR`s traced to ONE root cause. Confirmed live: `'now'`, `'today'`,
   `'tomorrow'`, `'yesterday'`, `'epoch'` all failed identically for
   `date`/`timestamp`/`timestamptz`/`time`/`timetz`, while `'infinity'`/
   `'-infinity'` already worked (a prior slice, #5(d-iv)). Upstream
   (`postgres/src/backend/utils/adt/datetime.c`'s `datetbl`, consumed by
   `DecodeDateTime`/`DecodeTimeOnly`) treats these as RESERV tokens resolved
   against the transaction's captured `now()`, not the live wall clock.
3. **`int2vector` materialization — NEWLY UNMASKED, not yet fixed.**
   `SELECT '1 2'::int2vector` works standalone, but
   `CREATE TABLE t AS SELECT '1 2'::int2vector AS x` raises `XX000: expected
   bytes for int2vector, got kind 3 (byte 0)` — the CTAS row-encode path
   expects an already-encoded byte value but receives the cast's raw Datum
   kind. This was invisible before slice 2's fix landed: the CTAS statement
   that reaches this column previously died on the earlier `'today'::date`
   column first. Textbook serially-masked-cause shape (`pattern_sibling_paths_must_agree`/
   the project's repeat "unmasking" precedent — M0134-0014, -0025, -0026).
4. **6 built-in range types have no `pg_range` catalog row.** `int4range`,
   `numrange`, `tsrange`, `tstzrange`, `daterange`, `int8range` all fail
   `WHERE t1.typtype = 'r' AND NOT EXISTS(SELECT 1 FROM pg_range ...)` — the
   type itself works (casts, comparisons), but `pg_range` (the catalog PG
   uses for `rngsubtype`/`rngsubopc`/`rngcanonical` metadata) was never
   populated for the built-ins. Contained-looking (6 static rows), but not
   picked up this loop — see Resume points.
5. **`pg_class.relam` is set backwards for two relkind families.** TOAST
   tables (`relkind='t'`) read `relam=0` where PG requires nonzero (26
   rows); views/sequences/foreign-tables/composites/partitioned-tables
   (`relkind IN ('S','v','f','c','p')`) read a nonzero `relam` where PG
   requires 0 (59 rows). Looks like one inverted condition in whichever
   builder assigns `relam`, but the exact call site was not located this
   loop.
6. **`pg_attribute.attnum > pg_class.relnatts` for 37 INDEX relations.**
   Every failing row's `attrelid` is an index OID (e.g. `pg_class_oid_index`,
   `onek_unique1`) — `pg_class.relnatts` for index relkinds appears
   understated relative to the real column count in `pg_attribute`.
7. **~500-row attribute type-metadata mismatch bucket.** `pg_attribute`
   columns typed `xid`, `_aclitem`, `timestamptz`, `interval`, `regtype`,
   `_float8`, and (the overwhelming majority) `information_schema`'s
   `sql_identifier`/`time_stamp` domain types disagree with their declared
   type's `typlen`/`typalign`/`typbyval` — almost entirely
   `information_schema` view columns. REFACTOR-tier: likely a
   domain-vs-base-type attribute-metadata propagation gap in the
   `information_schema.sql` view-column builder, not sized further this
   loop.

### What this means for scoping

Unlike the M0134 cases with one absent subsystem and no narrow slice
(`tstypes.sql`, M0134-0181), this file had a **real, independently
verifiable, immediately fixable** bug (bucket 2) sitting alongside several
REFACTOR-tier buckets — the RESERV keyword gap needed no prerequisite and
its blast radius (10 call sites, all funneling through 4 shared parsing
functions) was fully mapped before editing. It was shipped. Buckets 1
(pg_proc exposure) and 7 (attribute metadata) are each their own
REFACTOR-tier milestone; buckets 3-6 are small enough to look contained but
were not sized in enough depth this loop to commit to "shippable next" — see
Resume points.

## What landed

Four new shared functions in `internal/executor/copy_text.go` —
`parseDateSpecialLiteral`, `parseTimestampSpecialLiteral`,
`parseTimeSpecialLiteral`, `parseTimeTZSpecialLiteral` — each recognising
the RESERV spellings DecodeDateTime/DecodeTimeOnly accept for their domain
(`'now'`/`'today'`/`'tomorrow'`/`'yesterday'`/`'epoch'`, plus the
already-supported `'infinity'`/`'-infinity'` folded in so callers have one
entry point instead of two). They resolve against a new `nowFromCtx(ctx)`
helper (`internal/executor/expr.go`, next to the existing
`timeZoneFromCtx`) — the statement's captured `ctx.Now`, matching how
`now()`/`current_timestamp`/`current_date`/`current_time` already resolve,
with a real-wall-clock fallback for the few `encodeValuePGCtx` callers that
pass a nil `ctx` (catalog/bootstrap row encoders, never on a live literal
path). `'today'`/`'tomorrow'`/`'yesterday'` truncate to midnight in UTC —
deliberately mirroring `current_date`'s own existing UTC-only
simplification (`expr.go`'s `"current_date"` FuncCall arm) rather than
attempting session-TimeZone-aware midnight math that no sibling call site
in this codebase currently does either; that gap is pre-existing and
untouched by this fix.

All 10 call sites across the literal/cast/COPY-adjacent input paths were
updated to call the new functions instead of the old infinity-only ones
(which are retired — `parseDateInfinityLiteral`/`parseTimestampInfinityLiteral`
no longer exist as separate names):

| site | file:function | before | after |
|------|---------------|--------|-------|
| typed literal `DATE '...'` | `expr.go:evalTypedStringLit` (`"date"`) | `parseDateInfinityLiteral(x.Value)` | `parseDateSpecialLiteral(x.Value, nowFromCtx(ctx))` |
| typed literal `TIMESTAMP(TZ) '...'` | `expr.go:evalTypedStringLit` (`"timestamp","timestamptz"`) | `parseTimestampInfinityLiteral(x.Value)` | `parseTimestampSpecialLiteral(x.Value, nowFromCtx(ctx), isTimestampTZTypeName(x.Type))` |
| typed literal `TIME '...'` | `expr.go:evalTypedStringLit` (`"time"`) | *(none)* | `parseTimeSpecialLiteral(x.Value, nowFromCtx(ctx))` |
| typed literal `TIME WITH TIME ZONE '...'` | `expr.go:evalTypedStringLit` (`"timetz"`) | *(none)* | `parseTimeTZSpecialLiteral(x.Value, nowFromCtx(ctx), timeZoneFromCtx(ctx))` |
| cast `::date` | `expr.go:evalCast` | `parseDateInfinityLiteral(s)` | `parseDateSpecialLiteral(s, nowFromCtx(ctx))` |
| cast `::timestamp(tz)` | `expr.go:evalCast` | `parseTimestampInfinityLiteral(...)` | `parseTimestampSpecialLiteral(..., nowFromCtx(ctx), tz)` |
| cast `::time` | `expr.go:evalCast` | *(none)* | `parseTimeSpecialLiteral(..., nowFromCtx(ctx))` |
| cast `::timetz` | `expr.go:evalCast` | *(none)* | `parseTimeTZSpecialLiteral(..., nowFromCtx(ctx), timeZoneFromCtx(ctx))` |
| `pg_input_is_valid(v, 'date')` | `expr.go:evalFuncCall` | `parseDateInfinityLiteral(v)` | `parseDateSpecialLiteral(v, nowFromCtx(ctx))` |
| `pg_input_is_valid(v, 'timestamp(tz)')` | `expr.go:evalFuncCall` | `parseTimestampInfinityLiteral(v)` | `parseTimestampSpecialLiteral(v, nowFromCtx(ctx), t=="timestamptz")` |
| `pg_input_is_valid(v, 'time'/'timetz')` | `expr.go:evalFuncCall` | *(none)* | inline `EqualFold(v, "now")` early-out |
| row-encode `timestamp(tz)` value | `codec.go:encodeValuePGCtx` | `parseTimestampInfinityLiteral(...)` | `parseTimestampSpecialLiteral(..., nowFromCtx(ctx), isTimestampTZTypeName(t.Name))` |
| row-encode `date` value | `codec.go:encodeValuePGCtx` | `parseDateInfinityLiteral(...)` | `parseDateSpecialLiteral(..., nowFromCtx(ctx))` |

`time`/`timetz` deliberately do **not** get a signature change on
`parseTimeString`/`parseTimeTZString` themselves — those two functions have
14 call sites total (COPY text, btree scalar keys, `pg_input_error_info`,
...) far beyond the literal-input surface, and most of them have no
sensible "current time" to resolve against. The new special-literal
functions intercept `'now'` **before** the general parse, exactly the same
shape the pre-existing infinity interception already used — no existing
call site's behavior changes except gaining the new keyword recognition.

New test: `internal/executor/date_time_reserv_literal_test.go` —
`TestDateTimeReservedLiteral`, 20 cases across all five domains (date, time,
timetz, timestamp, timestamptz) plus `pg_input_is_valid`, each asserted
**self-consistently within one statement** (e.g. `'today'::date =
current_date`) since the values are inherently time-relative.

## Resume points

- **Bucket 1 (pg_proc/regproc exposure)** — own milestone, REFACTOR-tier.
  Resume: make the live `pg_proc` heap scan (or an in-memory index built
  from it at connection/catalog-load time) consume
  `internal/initdb.pgProcInitialEntries()`'s full 3397+11-row set instead of
  `catalog.builtinProcsByName`'s 32-entry curated map, for both the table
  scan (`SELECT * FROM pg_proc`) and the `regproc`/`regprocedure` cast
  fallback (`internal/executor/reg_identifier.go`'s `"regproc",
  "regprocedure"` arm — the `catalog.LookupBuiltinProc` call). Likely a
  visibility/heap-scan wiring gap (the heap file IS being written correctly
  at initdb time) rather than a missing-data problem.
- **Bucket 3 (int2vector CTAS)** — resume at the `CREATE TABLE ... AS
  SELECT` row-materialization path (wherever a `SELECT` projection's `Datum`
  is handed to the target table's row encoder) — an int2vector-cast Datum
  (`Kind 3`, i.e. `KindString` per `internal/executor/datum.go`'s `DatumKind`
  iota — confirm exact value) reaches the encoder un-normalized where a
  bytes-encoded value is expected.
- **Bucket 4 (pg_range for built-ins)** — 6 static rows
  (`int4range`/`numrange`/`tsrange`/`tstzrange`/`daterange`/`int8range`);
  resume wherever `pg_range` catalog rows are seeded/built (grep
  `internal/initdb` and `internal/catalog` for `pg_range`) and add the
  built-in range→subtype/subopc mappings PG's `pg_range.dat` carries.
- **Bucket 5 (relam sign flip)** — resume at whatever assigns
  `pg_class.relam` for a heap/TOAST/virtual relkind row; the condition
  looks inverted (`relkind IN (storage-having) → 0` vs `relkind IN
  (storage-less) → nonzero`, backwards from PG's `RELKIND_HAS_STORAGE`
  rule, `postgres/src/include/catalog/pg_class.h`).
- **Bucket 6 (index attnum > relnatts)** — resume at wherever
  `pg_class.relnatts` is computed/reported for `relkind IN ('i','I')`.
- **Bucket 7 (attribute type-metadata mismatch)** — REFACTOR-tier, own
  milestone; resume at the `information_schema.sql` view-column builder's
  attribute-metadata propagation (attlen/attalign/attbyval for domain-typed
  view columns like `sql_identifier`/`time_stamp`).
- Re-arm this case (re-run `scripts/pg-regress-runner.sh -v type_sanity`)
  once bucket 1 OR bucket 3 lands — either should visibly move the 9
  `^+ERROR` count for the first time since this loop's fix.

## Gates run

- `scripts/pg-regress-runner.sh -v type_sanity` (before/after; 1151 diff
  lines / 9 `^+ERROR` both times — the `'today'` error was replaced 1:1 by
  the newly-unmasked `int2vector` error, not eliminated outright).
- `go build ./...` clean.
- `go test ./internal/executor/...` — full package, including the new
  `TestDateTimeReservedLiteral` and the pre-existing
  `TestTimestampInfinityLiteral`/`TestTimestampInfinityWireCodec`/
  `TestTimestampSubInfinity`/`TestTimestampCrossCastLeavesInfinityAlone` —
  all PASS, confirming the infinity-literal behavior these tests pin was
  preserved exactly by folding it into the new shared functions.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — full
  units suite PASS (`internal/initdb` 477s, `cmd/goopg` 82s, everything
  else cached/fast).
- `make check-testport-inventory` PASS.
- `make regen-testport` — clean regen (6-file diff: CSV status flip +
  derived doc/report counts, `regress-sql` 160→161 failed / 9→8 not-tried).
- `make ralph-state-guard` PASS.
