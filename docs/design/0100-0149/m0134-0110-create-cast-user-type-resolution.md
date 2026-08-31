# M0134-0110 — `create_cast.sql`: CREATE CAST user-type OID resolution

Status: PARKED (`failed`, sized live 2026-08-24). Four contained,
oracle-verified fixes landed; the case's dominant remaining diff is a
structurally larger, unrelated feature gap (the `::` cast evaluator never
consults the user-cast/`pg_cast` registry), not fixed here.

## What this test exercises

`postgres/src/test/regress/sql/create_cast.sql` covers `CREATE CAST` in all
three forms (`WITHOUT FUNCTION`, `WITH INOUT`, `WITH FUNCTION`), `DROP CAST`,
cast-context enforcement (explicit vs. implicit — an explicit-only cast must
not be usable in an implicit position such as a function-argument match),
`WITH FUNCTION` execution semantics (the cast actually calls the named
function), and `pg_depend` bookkeeping for a cast's type/function
dependencies. All of it is exercised over a hand-built user type
(`casttesttype`) created with the base-type `CREATE TYPE name (INPUT=...,
OUTPUT=..., ...)` form, first declared as a forward-reference shell
(`CREATE TYPE casttesttype;`) and completed afterward.

## Root cause found and fixed

### 1. `castTypeOIDMatch` collapsed every user type to `text`'s OID

`validateCreateCast`/`castTypeOIDMatch` (`internal/executor/operators_ddl.go`)
compared source/target type names via the pure `catalog.TypeNameToOID(string)`
helper. That helper's `switch` has a `default: return OIDText` arm — a "safe
fallback" for any name it doesn't recognize, which includes **every
user-defined type**. So `castTypeOIDMatch("text", "casttesttype")` computed
`TypeNameToOID("casttesttype") == OIDText == TypeNameToOID("text")` and
concluded the two names were the same type, making `CREATE CAST (text AS
casttesttype) WITHOUT FUNCTION` always fail with PG's own
"source data type and target data type are the same" error — for the
*opposite* reason PG would ever raise it.

### 2. `DROP CAST`'s "type does not exist" check had the same collapse

`dropCompatCanonicalType` (same file) is a small hand-written table mapping
PG's canonical builtin spellings; any other name returns `""`, and the DROP
CAST handler treated `""` as "the type does not exist" — again true for any
user-defined type, so `DROP CAST (text AS casttesttype)` always failed even
right after a successful `CREATE CAST`.

### 3. Underlying catalog bug: base/shell `CREATE TYPE` never got a real OID

Both (1) and (2) needed a catalog-aware resolver as the fix, which surfaced a
deeper bug while wiring it in. `execCreateType`'s non-enum, non-range,
non-composite-with-fields branch — the one a base type
(`CREATE TYPE name (INPUT=..., OUTPUT=..., ...)`) reaches — calls the
bare-name `cat.RegisterCompositeType(name, dbOid)` "so DROP TYPE can succeed
without error". That function
(`internal/catalog/catalog.go`) wrote **only** the boolean
`compositeTypeNames` existence-tracking map. It never touched
`compositeTypes`, the OID-bearing map that `LookupCompositeType` (and
therefore `resolveUserTypeOID`, and everything built on it) actually reads.
Every base/shell user type therefore had a name that "existed" per one
registry but **no OID anywhere** per the other — any OID-keyed lookup for
such a type silently came back empty, which is a much wider landmine than
just this test case (it also explains why `pg_type` never gained a row for
`casttesttype` — confirmed live: `SELECT oid FROM pg_type WHERE
typname='casttesttype'` returns 0 rows even after successful creation and use
of the type in later `::casttesttype` casts).

### 4. `castTypeOIDMatch` had no binary-coercibility check

The function's own pre-existing doc comment already flagged this as
deferred: PG's `IsBinaryCoercibleWithCast` lets a `WITH FUNCTION` cast's
declared argument/return type be merely binary-coercible to (not identical
to) the cast's source/target, via an *already-registered* `WITHOUT FUNCTION`
cast between the two. `create_cast.sql` exercises exactly this: line 37
registers `CREATE CAST (text AS casttesttype) WITHOUT FUNCTION AS IMPLICIT`
and never drops it, so by line 63's `CREATE CAST (int4 AS casttesttype) WITH
FUNCTION bar_int4_text(int4) AS IMPLICIT` (where `bar_int4_text` returns
`text`, not `casttesttype`), PG accepts it via that earlier binary-coercion
registration. Without the check, goopg's now-correct OID comparison rejected
it as a genuine mismatch — a regression relative to the pre-fix accidental
(wrong-reason) leniency.

## Fix

- `castResolveTypeOID(im, name)` (new): tries `resolveUserTypeOID` (the
  enum/domain/composite registries — the last of which is exactly what (3)
  above fixes to also cover base types) before falling back to
  `catalog.TypeNameToOID`, and returns `0` — not the `OIDText` safe default —
  for a name recognized by neither, so an unrecognized name can never
  falsely compare equal to `text`. The only name `TypeNameToOID`'s switch
  itself maps to `OIDText` is the literal `"text"` case; every other input
  reaches that value only through the default arm, which an `EqualFold`
  guard distinguishes.
- `castTypeOIDMatch(im, a, b)` now takes the live catalog and uses
  `castResolveTypeOID`, plus a new `castUserBinaryCoercible(im, a, b)` check
  (a registered `CREATE CAST ... WITHOUT FUNCTION`, `pg_cast.castmethod =
  'b'`, in either direction) as an alternate match condition — mirroring
  `IsBinaryCoercibleWithCast`. `validateCreateCast` threads `im` through from
  its one call site in the `"cast"` DDL-dispatch case.
- The DROP CAST handler now calls `isKnownUserType(im, name)` (same
  `resolveUserTypeOID` check) before raising "type does not exist" for a name
  `dropCompatCanonicalType` doesn't recognize.
- `catalog.InMemory.RegisterCompositeType` (the bare-name registrar) now also
  allocates a real `*CompositeType{OID, ArrayOID, RelOID}` entry in
  `compositeTypes`, mirroring `RegisterCompositeTypeWithFields`, so a base
  type gets an OID any lookup can find. This function has exactly one call
  site (`execCreateType`'s base-type fallback), so the change is contained.

## Verified

Manual psql session against a throwaway server reproduced the fix live —
`CREATE CAST (text AS casttesttype) WITHOUT FUNCTION`, `DROP CAST (text AS
casttesttype)`, and re-`CREATE CAST ... AS IMPLICIT` all now succeed where
they previously raised false errors. Full oracle diff for `create_cast.sql`
shrank from 119 to 106 lines (the three false-error lines and their
associated cascading `LINE`/`^` pointer lines are all gone); the remaining
lines are the genuinely deferred gap below.

`go build ./...` clean. `go test ./internal/catalog/... ./internal/executor/...`
PASS (`TestValidateCreateCast` unaffected in behavior for builtin types, `nil`
catalog argument). `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS (full unit suite).

## Sizing verdict: PARKED

The remaining ~106-line diff is dominated by one structural gap: **the `::`
CAST expression evaluator never consults the user-cast (`pg_cast`) registry
at all.** Concretely, all still observed:

- `SELECT 1234::int4::casttesttype` with **no cast registered** wrongly
  succeeds (returns `1234`) instead of raising PG's `cannot cast type
  integer to casttesttype`.
- `SELECT casttestfunc('foo'::text)` wrongly succeeds even when **no cast at
  all** exists from `text` to `casttesttype` (goopg's function-argument
  matcher implicitly coerces regardless), and still wrongly succeeds later
  when an EXPLICIT-only cast is the only one registered (cast-context
  enforcement is entirely absent from function-overload resolution).
- A `WITH FUNCTION` cast's function is never actually invoked at cast-eval
  time: `1234::int4::casttesttype` through a registered
  `int4_casttesttype(int4)` function returns `1234`, not the function's real
  transformed output (`foo1234`) — the evaluator appears to run a generic
  same-representation fallback regardless of what cast is registered.
- `pg_depend` records 0 rows for a cast's type/function dependencies (PG:
  3 rows).
- Two cosmetic NOTICE messages ("... is only a shell", "drop cascades to
  cast from ...") are not emitted.

Threading real cast dispatch through every `::`/`CAST(...)` site in
`internal/executor/expr.go` is a genuinely REFACTOR-tier expression-evaluator
feature — a new code path used everywhere types are coerced, not a local bug
— so it stays out of this contained fix. Parked per the established M0134
pattern (cf. M0134-0108/-0109): the four independent, oracle-verified
sub-fixes above are landed; the rest is ledgered.

## Deferred (see `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0110)

- `::`/`CAST(...)` evaluator: no pg_cast-existence check, no cast-context
  (explicit/assignment/implicit) enforcement anywhere (including function
  overload argument matching), no `WITH FUNCTION` dispatch at execution time.
- `pg_depend` rows for a cast's dependency on its type(s)/function.
- The two shell-type/cascade-drop NOTICE messages.
