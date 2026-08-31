# M0134-0108 — `create_aggregate.sql`: aggregate `regproc`/`regprocedure` resolution + `internal` pseudo-type OID

Status: PARKED (`failed`, sized live 2026-08-24). Two contained, oracle-verified
fixes landed; the case's dominant remaining diff is a distinct, larger feature
gap (CREATE AGGREGATE validation) tracked in the deferral ledger, not fixed here.

## What this test exercises

`postgres/src/test/regress/sql/create_aggregate.sql` covers `CREATE AGGREGATE`,
`CREATE OR REPLACE AGGREGATE`, `ALTER AGGREGATE ... RENAME`, `COMMENT ON
AGGREGATE`, moving-aggregate (`MSTYPE`/`MSFUNC`/`MINVFUNC`) options, and a large
block of negative validation tests, followed by `pg_aggregate` catalog SELECTs
keyed off `'myavg'::REGPROC`.

## Root cause found and fixed

`CREATE AGGREGATE myavg (...)` registered the aggregate correctly — both in
`catalog.InMemory.userAggregates` (via `RegisterUserAggregate`) and as a real
`pg_proc`/`pg_aggregate` heap row (via `writeAggregateCatalogRows`, per the
B2.2 slice 2 routine funnel). A plain `SELECT * FROM pg_proc WHERE proname =
'myavg'` found the row. But `SELECT 'myavg'::REGPROC` — and therefore every
`pg_aggregate` query in the test keyed off `WHERE aggfnoid =
'myavg'::REGPROC` — raised `function "myavg" does not exist`.

The reason: `regproc`/`regprocedure` name→OID resolution never consults
`InMemory.userAggregates`. It only checks two sources:

1. `ctx.Catalog.Routines()` — the live `CREATE FUNCTION`/`CREATE PROCEDURE`
   registry (`internal/catalog/routines.go`).
2. `catalog.LookupBuiltinProc` — the curated built-in `pg_proc.dat` table.

A `CREATE AGGREGATE` routine lands in neither. This resolution logic exists
in **two independent implementations** (a known recurring hazard — Hard-won
Rule #2, "sibling paths must change together"):

- `internal/executor/reg_identifier.go`'s `regIdentifierInput` (the shared
  primitive used by array-element casts, heap column coercion, etc.)
- `internal/executor/expr.go`'s `evalExprSlot` `*optimizer.CastExpr` arm — a
  **duplicate** implementation, called out by name in `regIdentifierInput`'s
  own doc comment as the "normal" path for a literal `'name'::regproc` cast
  reached through the interpreted expression evaluator (as opposed to a bound
  `EXECUTE` parameter, which goes through `CoerceParamToDeclaredType` →
  `regIdentifierInput` directly).

Both had to be fixed, plus the reverse (OID→name) rendering direction:

- `internal/executor/reg_identifier.go`'s `regOutShared`, `case "regproc"` —
  used by `RegOut`/`RegOutRenderer` for column output and `::regproc`-typed
  value formatting.

## Fix

Added `catalog.Catalog.LookupUserAggregateByOID(oid uint32) (*UserAggregate,
bool)` — a new interface method alongside the existing
`LookupUserAggregateByName`, implemented on `*InMemory` as a linear scan over
the (small, user-created-only) `userAggregates` map, mirroring
`LookupUserOperatorByOID`'s equivalent tradeoff for a similarly small
registry.

Wired it as the final fallback (after builtin, after `Routines()`) at all
three sites:

- `regIdentifierInput`'s `regproc`/`regprocedure` case (input)
- `evalExprSlot`'s `CastExpr` `regproc`/`regprocedure` arm (input, the
  sibling that was actually being hit for this test's plain literal casts)
- `regOutShared`'s `regproc` case (output)

`regprocedure` output rendering (`format_procedure`, argument-list-qualified
names) was intentionally left unchanged — it resolves through
`catalog.RegprocedureNameParts`, a separate helper keyed off `Routines()`
only; extending it to aggregates is part of the larger deferred bucket
below, not this contained fix.

## Second fix: `internal` pseudo-type OID

Once the aggregate itself resolved, `aggtranstype::regtype` rendered `text`
instead of `internal` for `stype = internal` (the standard PG idiom for an
aggregate whose transition state is a C-level in-memory structure too complex
to represent as a plain SQL value — `numeric_avg_accum`'s use in this exact
test file). `catalog.TypeNameToOID` had no `case "internal"` at all, so the
name silently fell into the function's `default: return OIDText // safe
fallback` branch.

The `internal` pseudo-type's `pg_type` row (OID 2281) was already fully
seeded (`internal/initdb/pg_type_seed_data.go:128`,
`internal/initdb/pg_type_bootstrap.go:224-226`) — only the name→OID lookup
table was missing the entry. Added `catalog.OIDInternal = 2281` and the
corresponding `TypeNameToOID` case.

## Verified

```
scripts/pg-regress-runner.sh --verbose create_aggregate
```
249 → 234 diff lines (both `'myavg'::REGPROC` catalog-SELECT blocks now match
byte-for-byte on the `aggfnoid`/`aggtranstype` columns; `aggserialfn`/
`aggdeserialfn` still show `-` instead of the real function names — see the
deferral ledger, bucket (1)).

`go test ./internal/catalog/... ./internal/executor/...` — clean.

## Deferred (see `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0108)

Six independent gaps remain in the file's diff, none touched by this fix:

1. `SERIALFUNC`/`DESERIALFUNC`/`PARALLEL`/moving-aggregate options are parsed
   (`internal/parser/ddl.go:1396`) then explicitly discarded — no
   `CreateAggregateStmt` fields exist for them, so
   `buildUserPGAggregateRow` hardcodes those `pg_aggregate` columns to
   zero/default regardless of what was declared.
2. Zero validation for serialfunc-without-deserialfunc, signature mismatches
   on serialfunc/deserialfunc/combinefunc, or an out-of-range `PARALLEL`
   value.
3. `CREATE OR REPLACE AGGREGATE` has no prior-definition comparison —
   `execCreateAggregate` unconditionally overwrites, never raising PG's
   "cannot change return type of existing function" / "cannot change routine
   kind".
4. `COMMENT ON AGGREGATE` is not a distinct parser case at all
   (`internal/parser/parser.go`'s `parseCommentOnTail` has zero `aggregate`
   references) — a nonexistent-aggregate comment silently no-ops instead of
   raising 42883.
5. `\da` mis-renders an ordered-set aggregate's result/argument-type columns
   (raw stype/argtype instead of the finalfunc's real return type + ORDER BY
   argument list).
6. The legacy quoted-identifier attribute spellings (`"Sfunc1"`, `"Stype"`,
   etc.) are silently accepted instead of raising PG's per-attribute
   `WARNING: aggregate attribute "%s" not recognized` + a terminal 42P13 when
   no valid stype survives.

Moving-aggregate support (bucket 1's `MSTYPE`/`MSFUNC`/`MINVFUNC` trio) is a
genuinely unimplemented aggregate variant, not just a missing check — the
executor's aggregate-evaluation path (`internal/executor/operators_join_agg.go`)
has no inverse-transition/windowed-aggregate machinery at all. Resume order
and file/function pointers are in the ledger row.
