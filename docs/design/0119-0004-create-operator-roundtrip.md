# CREATE OPERATOR round-trip in pg_dump (DU-002 slice 406)

- **Milestone/Spec:** M0119-0004 (pg_dump 002–010 TAP / DU-002 catalog-view parity battery)
- **Status:** accepted
- **Loop:** #30 (verifying/landing work started by a prior backgrounded loop)

## Problem

`CREATE OPERATOR` was a name-registration-only compat no-op
(`execCompatNoop`'s `"operator"` case only fed the generic
`DropCompatObject`/`RegisterCompatObject` registry so `DROP OPERATOR` could
find the name later — M0097-regress). It never touched `pg_operator`, so
`pg_catalog.pg_operator.VirtualRows` was unconditionally `func() [][]string {
return nil }`, correctly empty only because no code path could ever populate
it (DU-002 slice 9's original rationale — "goopg defines no user operators" —
was true by construction, not by design). pg_dump's `getOperators` therefore
always read 0 rows and a user's `CREATE OPERATOR` was silently lost on
dump/restore.

## Fix

- **Parser** (`internal/parser/ddl.go`, `parseCreate`'s operator branch): the
  existing LEFTARG/RIGHTARG key-value scanner over the parenthesised
  `CREATE OPERATOR name (...)` option list gains a `function`/`procedure` key
  (PG's `operator_def_arg` treats the two as synonyms — `operatorcmds.c`). The
  grammar position takes only a bare, optionally schema-qualified name (no
  parenthesised arg-type list — PG infers the operator's signature from
  LEFTARG/RIGHTARG, unlike CAST/TRANSFORM's `WITH FUNCTION`). Captured on the
  existing `CompatNoopStmt` as a new `OpFuncName ObjectName` field.
- **Catalog** (`internal/catalog/catalog.go`): new `UserOperator` type
  (OID, Name, NamespaceOID, LeftType, RightType, FuncOID, Owner) plus
  `RegisterUserOperator` / `DropUserOperator` / `ListUserOperators`, keyed like
  `dropCompat`'s operator key (`"<schema>.<name>(<leftType>,<rightType>)"`,
  lowercased) so the same symbol can overload across schemas/arg-type pairs —
  unlike `Cast`/`Transform`, which forbid duplicate keys outright.
  `pg_operator.VirtualRows` now renders one row per registered operator
  (`oprkind='b'`, `oprcanmerge`/`oprcanhash='f'` — MERGES/HASHES not parsed
  yet, `oprcode` from `FuncOID`, `oprcom`/`oprnegate`/`oprrest`/`oprjoin`
  literal `"0"` — COMMUTATOR/NEGATOR/RESTRICT/JOIN not parsed yet).
- **Executor** (`internal/executor/operators_ddl.go`): `execCompatNoop`'s
  `"operator"` case, when `OpFuncName` is present, resolves the function OID
  (user routine registry first via `Routines().LookupByName`, falling back to
  `catalog.LookupBuiltinProc` for a builtin — mirrors CREATE CAST's and
  `resolveTransformFunc`'s identical fallback) and calls
  `RegisterUserOperator`. `execDropCompat`'s operator case now also calls
  `DropUserOperator` alongside the existing `DropCompatObject` so a dropped
  operator stops appearing in a subsequent dump (mirrors `DropCast`/
  `DropTransform`).
- New builtin `int4eq` (OID 65) curated in `builtinProcsByName` so a test
  fixture's `FUNCTION = int4eq` resolves to a real OID (PG's own `=` operator
  over `int4` uses this function, `pg_operator.dat` oid 96).

## Scope / limitations

Only the skeleton clauses (FUNCTION/PROCEDURE, LEFTARG, RIGHTARG) are parsed
and modeled. `COMMUTATOR`, `NEGATOR`, `RESTRICT`, `JOIN`, `MERGES`, `HASHES`,
and unary (prefix) operators are not — a `CREATE OPERATOR` using any of those
clauses round-trips its FUNCTION/LEFTARG/RIGHTARG only, silently dropping the
rest. goopg does not execute the operator itself (no expression-evaluator
dispatch through a user FUNCTION for a custom operator symbol) — this is
dump-fidelity only, matching the trigger-roundtrip precedent.

## Blast radius

- `pg_operator.VirtualRows` renders extra rows only when `ListUserOperators()`
  is non-empty; with none registered (the pre-existing case, and every
  existing regress/TPC-H fixture) the view stays byte-identical (`nil`).
- New builtin `int4eq` (OID 65) is additive to `builtinProcsByName`; no
  existing lookup by that name existed before.

## Oracle

Mirrors `postgres/src/backend/commands/operatorcmds.c` (`DefineOperator`,
`operator_def_arg` synonym handling) and `postgres/src/bin/pg_dump/pg_dump.c`
`getOperators`/`dumpOpr` (the FUNCTION clause via `convertRegProcReference`,
which truncates the regprocedure text at the first unquoted `(` — bare name
only; LEFTARG/RIGHTARG spelled out via `format_type`, e.g. `int` →
`integer`). Compared against a live PG 18.3 instance.

## Gates

- **DU-002 slice 406** in `TestPort_PgDumpConnectionSetup`:
  `CREATE OPERATOR public.~~ (FUNCTION = int4eq, LEFTARG = int, RIGHTARG =
  int)` re-emits the exact `CREATE OPERATOR public.~~ (\n    FUNCTION =
  int4eq,\n    LEFTARG = integer,\n    RIGHTARG = integer\n);` plus a trailing
  `ALTER OPERATOR public.~~ (integer, integer) OWNER TO` line (the latter is
  pg_dump's own generic owner-emission machinery reading `oprowner` — no
  goopg-side rendering code needed), verified vs real pg_dump 18.3.
- `internal/catalog` + `internal/executor` + `internal/parser` suites PASS;
  `go build ./...` / `go vet ./...` clean; TPC-H spotcheck Q12=2/Q13=33 PASS;
  pgbench smoke = pre-commit hook.

## Still open under M0119-0004

COMMUTATOR/NEGATOR/RESTRICT/JOIN/MERGES/HASHES clauses; unary (prefix)
operator forms; `regoper`/`regoperator` OID→name resolution (no column is
typed `regoper` yet, so no observable gap); further pg_dump 002–010 catalog
parity slices.
