# CREATE OPERATOR round-trip in pg_dump (DU-002 slice 406/407)

- **Milestone/Spec:** M0119-0004 (pg_dump 002–010 TAP / DU-002 catalog-view parity battery)
- **Status:** accepted
- **Loop:** #30 (slice 406, verifying/landing work started by a prior
  backgrounded loop); #32 (slice 407, COMMUTATOR/NEGATOR/RESTRICT/JOIN/
  MERGES/HASHES + unary operators); #33 (`ALTER OPERATOR ... SET (...)`,
  closing the slice-407 ledger follow-up)

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

Only the skeleton clauses (FUNCTION/PROCEDURE, LEFTARG, RIGHTARG) were parsed
and modeled in slice 406. goopg does not execute the operator itself (no
expression-evaluator dispatch through a user FUNCTION for a custom operator
symbol) — this is dump-fidelity only, matching the trigger-roundtrip
precedent.

## Slice 407: COMMUTATOR/NEGATOR/RESTRICT/JOIN/MERGES/HASHES + unary operators

Extends slice 406's skeleton to the remaining `CREATE OPERATOR` clauses and
unary (prefix) operator forms, closing the gap the slice-406 ledger row
recorded.

- **Parser** (`internal/parser/ddl.go`, `parser.go`): the key-value scanner
  gains `restrict`/`join` (bare, optionally schema-qualified function name —
  same grammar shape as `function`), `commutator`/`negator` (an operator
  reference via new `parseOperatorRefName`, which accepts both a bare
  operator symbol and pg_dump's emitted `OPERATOR(schema.op)` form —
  `getFormattedOperatorName`, pg_dump.c), and the bare `merges`/`hashes`
  flags (no `=` value, though `= true`/`= false` is also tolerated). LEFTARG
  is now optional — an absent LEFTARG models a unary (prefix) operator
  (`ArgTypes[0] == ""`); RIGHTARG is still required (PG14+ removed postfix
  operators outright). New fields on `CompatNoopStmt`: `OpCommutatorName`,
  `OpNegatorName`, `OpRestrictFuncName`, `OpJoinFuncName`, `OpCanMerge`,
  `OpCanHash`.
- **Catalog** (`internal/catalog/catalog.go`): `UserOperator` gains
  `CommutatorOID`/`NegatorOID`/`RestrictOID`/`JoinOID`/`CanMerge`/`CanHash`.
  New `LookupUserOperator` (by identity key), `LookupUserOperatorByOID`, and
  `EnsureUserOperatorShell` — the last mirrors PG's `OperatorShellMake`
  (`pg_operator.c`): a `COMMUTATOR`/`NEGATOR` clause may forward-reference an
  operator that doesn't exist yet, so a placeholder row (`FuncOID == 0`) is
  inserted purely to mint a stable OID; the operator's own later `CREATE
  OPERATOR` statement fills it in for free, since `RegisterUserOperator` is
  idempotent by the same `(schema,name,leftType,rightType)` key and therefore
  reuses the shell's OID. `pg_operator.VirtualRows` now renders
  `oprcanmerge`/`oprcanhash` from the new fields, `oprcom`/`oprnegate`/
  `oprrest`/`oprjoin` from the resolved OIDs, `oprkind='l'` when `LeftType==
  ""` (unary), and skips any row whose `FuncOID==0` — an unfilled shell,
  mirroring `dumpOpr`'s own `if (!OidIsValid(oprinfo->oprcode)) return;`.
- **Executor** (`internal/executor/operators_ddl.go`): `execCompatNoop`'s
  `"operator"` case rejects a missing RIGHTARG (postfix — 42P13, "Postfix
  operators are not supported."), resolves RESTRICT/JOIN exactly like
  FUNCTION, and resolves COMMUTATOR/NEGATOR via a two-pass forward-reference
  scheme mirroring PG's `get_other_operator`/`OperatorShellMake`/
  `OperatorUpd` (`pg_operator.c`): look up an existing (real or shell)
  operator by identity first, detect self-linkage (an operator naming
  itself, valid for a symmetric COMMUTATOR like `=` but always rejected for
  NEGATOR — "operator cannot be its own negator"), else forward-declare a
  shell. After linking this operator, it back-patches the other side's own
  `CommutatorOID`/`NegatorOID` if not already set, so a pair of operators can
  be defined in either order while only one statement restates the link.
  Also ports PG's `OperatorValidateParams` (`operatorcmds.c`) attribute
  gating: COMMUTATOR/JOIN/MERGES/HASHES require a binary operator (both
  LEFTARG and RIGHTARG); NEGATOR/RESTRICT/JOIN/MERGES/HASHES require a
  boolean-returning FUNCTION (all 42P13).

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
- **Slice 407** (new tests, no existing fixture exercises these clauses):
  `TestParseCreateOperatorExtendedClauses`/`TestParseCreateOperatorUnary`
  (parser); `TestCreateOperatorCommutatorNegatorBackPatch`/
  `TestCreateOperatorSelfCommutator`/`TestCreateOperatorSelfNegatorRejected`/
  `TestCreateOperatorUnaryAndValidation` (executor, `create_operator_test.go`)
  cover the two-pass shell resolution (including reuse of a shell's OID on
  fill-in and shell-exclusion from `pg_operator.VirtualRows` until filled),
  self-commutator, self-negator rejection, unary/prefix operators, postfix
  rejection, and the binary/boolean attribute-gating rules.
- `internal/catalog` + `internal/executor` + `internal/parser` suites PASS;
  `go build ./...` clean; `gofmt -l` reports pre-existing go1.25/1.26 drift
  only (verified against `git show HEAD:<file>` — every touched file already
  failed `gofmt -l` before this change); TPC-H spotcheck Q12=2/Q13=33 PASS;
  pgbench smoke = pre-commit hook.

## Loop #33: `ALTER OPERATOR name (left_type, right_type) SET (...)`

Closes the slice-407 ledger follow-up: PG's post-creation attribute-edit form
(`AlterOperator`, `operatorcmds.c`) was previously swallowed as a pure no-op
by the generic `ALTER VIEW/SCHEMA/COLLATION/.../OPERATOR/...` compat-stub
loop in `parseAlter` — a user-written `ALTER OPERATOR foo(int,int) SET
(RESTRICT = ...)` silently did nothing instead of actually changing the
operator.

- **Parser** (`internal/parser/ddl.go`'s `parseAlter`): a new branch checked
  *before* the generic stub loop. `ALTER OPERATOR CLASS|FAMILY ...` (a
  different object type entirely) and any `ALTER OPERATOR name(...)` tail
  that is not `SET ( ... )` — `OWNER TO`, `SET SCHEMA`, or anything else —
  fall back to the same consume-and-succeed no-op the stub gave the whole
  statement before (goopg does not track per-operator ownership/namespace
  *changes at ALTER time*, only at CREATE via `UserOperator.Owner`/
  `NamespaceOID`), so nothing that used to parse now errors. Only the
  `SET ( option = value, ... )` def-list form produces the new
  `AlterOperatorSetStmt` AST node (`ast.go`), reusing `parseOperatorRefName`
  for COMMUTATOR/NEGATOR and the same bare/`=value` MERGES/HASHES scanning
  CREATE OPERATOR already has. LEFTARG/RIGHTARG/FUNCTION/PROCEDURE inside the
  SET list raise a syntax error (immutable after CREATE, matching
  `AlterOperator`'s own rejection).
- **Planner** (`internal/planner/planner.go`): `AlterOperatorSetStmt` added
  to the DDL-passthrough case list (a statement type the planner didn't know
  about would otherwise raise `0A000 unsupported statement type`).
- **Executor** (`internal/executor/operators_ddl.go`): new
  `execAlterOperatorSet` looks up the existing `UserOperator` (42883 if not
  found) and mirrors `AlterOperator`'s per-attribute rules exactly:
  - RESTRICT/JOIN may be changed freely, including cleared via `= NONE`.
  - COMMUTATOR/NEGATOR/MERGES/HASHES may only be **set** if not already set;
    restating the identical value is a no-op (allowed), a genuinely
    different value is rejected (42P13 "operator attribute ... cannot be
    changed if it has already been set"). Self-negation is rejected the same
    way CREATE OPERATOR rejects it.
  - The CREATE OPERATOR case's inline `resolveFn`/`resolveOther` closures
    (RESTRICT/JOIN function resolution; COMMUTATOR/NEGATOR two-pass
    forward-reference/shell resolution and back-patching) were extracted
    into shared `(*ddlOp).resolveOperatorSupportFunc` /
    `(*ddlOp).resolveOperatorRef` methods so CREATE and ALTER share one
    resolution path instead of duplicating the logic (a repeat divergence
    would be exactly the "sibling paths must agree" failure mode this
    project has hit before).
- New builtins `eqsel`/`eqjoinsel`/`neqjoinsel` (OIDs 101/105/106) curated in
  `builtinProcsByName` — PG's own `=` operator's `oprrest`/`oprjoin` — so a
  RESTRICT=/JOIN= test fixture resolves to a real OID instead of silently 0
  (same "extend as new fixtures need more builtins" pattern as `int4eq`).

### Scope / limitations

No `pg_dump` TAP fixture exercises this statement — `pg_dump` never emits
`ALTER OPERATOR ... SET (...)`; every attribute a dump needs is captured by
the forward-reference shell mechanism already in `CREATE OPERATOR` itself.
This is real DDL semantics for a user/migration script to type directly, not
a dump-parity slice. Ownership is not enforced (no `object_ownercheck`
equivalent — goopg's DDL surface has no real per-session role identity,
matching every other operator DDL arm).

### Gates

`TestParseAlterOperatorSet`/`TestParseAlterOperatorSetRestrictNone`/
`TestParseAlterOperatorSetImmutableAttr`/`TestParseAlterOperatorOwnerToIsNoop`
(parser, `op_compat_test.go`); `TestAlterOperatorSetRestrictJoin`/
`TestAlterOperatorSetCommutatorNegatorOnceOnly`/
`TestAlterOperatorSetMergesHashesOnceOnly`/
`TestAlterOperatorSetMissingOperator` (executor, `create_operator_test.go`).
`go build ./...` clean; `go vet` parser/catalog/executor/planner clean;
`internal/parser`+`internal/catalog`+`internal/executor`+`internal/planner`
suites PASS; `gofmt -l` flags only the same pre-existing files as slice 407
(verified via `git stash`); TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke
= pre-commit hook.

## Still open under M0119-0004

`regoper`/`regoperator` OID→name resolution (no column is typed `regoper`
yet, so no observable gap); further pg_dump 002–010 catalog parity slices.
