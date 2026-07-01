# CREATE OPERATOR round-trip in pg_dump (DU-002 slice 406/407)

- **Milestone/Spec:** M0119-0004 (pg_dump 002–010 TAP / DU-002 catalog-view parity battery)
- **Status:** accepted
- **Loop:** #30 (slice 406, verifying/landing work started by a prior
  backgrounded loop); #32 (slice 407, COMMUTATOR/NEGATOR/RESTRICT/JOIN/
  MERGES/HASHES + unary operators); #33 (`ALTER OPERATOR ... SET (...)`,
  closing the slice-407 ledger follow-up); #34 (`CREATE OPERATOR FAMILY`,
  slice 408); #35 (`CREATE OPERATOR CLASS` pg_opclass population, slice 409);
  #37 (pg_amop/pg_amproc member store, slice 411); #38
  (`regoperator`/`regprocedure` schema-qualification, slice 412)

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

## Loop #34: `CREATE OPERATOR FAMILY name USING method` (DU-002 slice 408)

New object family — until this loop `CREATE OPERATOR FAMILY` had **no parse
path at all** (only `ALTER OPERATOR FAMILY ... OWNER TO` fell into the
generic `ALTER VIEW/SCHEMA/COLLATION/.../OPERATOR CLASS|FAMILY/...`
compat-stub no-op in `parseAlter`), so `pg_opfamily`'s virtual view was
unconditionally empty by construction and pg_dump's `getOpfamilies` always
read 0 rows. This is upstream's own bare `002_pg_dump.pl` fixture
(`'CREATE OPERATOR FAMILY dump_test.op_family'`) — unlike `CREATE OPERATOR
CLASS`, PG's `CREATE OPERATOR FAMILY` grammar has **no `AS` clause**: the
family starts empty; `OPERATOR`/`FUNCTION` members are added later via a
separate `ALTER OPERATOR FAMILY ... ADD` statement (`CreateOpFamily`,
`opfamilycmds.c`).

- **Parser** (`internal/parser/ddl.go`'s `parseCreate`, `"operator"` case):
  a new `p.acceptIdentKeyword("family")` branch checked right after the
  pre-existing `CLASS` branch (before falling into the bare `CREATE
  OPERATOR` symbol-name parse). New `parseCreateOpFamilyTail` parses
  `[schema.]name USING method` via `p.parseObjectName()` (unlike
  `CreateOpClassStmt.Name`, which only ever captured a single unqualified
  token — a pre-existing, out-of-scope limitation left alone) and stashes
  the method name on a new `CompatNoopStmt.OpFamilyMethod string` field,
  reusing the same "parse into `CompatNoopStmt`, decorate with
  `ObjType`/`ObjName`" pattern every other compat-registry DDL statement in
  this file already follows (`CREATE OPERATOR`, `CREATE CAST`, `CREATE
  CONVERSION`, ...).
- **Catalog** (`internal/catalog/catalog.go`): new `UserOperatorFamily`
  struct (`OID`/`Name`/`NamespaceOID`/`Method`/`Owner`, with
  `OwnerOrDefault`/`NamespaceOIDOrDefault` mirroring `UserOperator`'s) +
  `RegisterUserOperatorFamily`/`DropUserOperatorFamily`/
  `ListUserOperatorFamilies`, keyed on `"<schema>.<name>/<method-oid>"` (PG
  scopes opfamily-name uniqueness per namespace *and* access method, so the
  key includes the method OID, not just schema+name). `pg_opfamily`'s
  `VirtualRows` now renders `ListUserOperatorFamilies()` instead of the
  hardcoded `nil`. New package-level `AccessMethodOIDByName` resolves
  `btree`/`hash`/`gist`/`gin`/`spgist`/`brin`/`heap` to their `pg_am.oid`
  (the same 7 rows `pg_am.VirtualRows` already serves) — a small helper
  mirroring the existing `LanguageNameToOID`.
- **Executor** (`internal/executor/operators_ddl.go`, `execCompatNoop`): a
  new `case "operator family":` resolves the method name via
  `catalog.AccessMethodOIDByName` (raising `42704 "access method %q does not
  exist"` for an unrecognized one — PG's own `get_index_am_oid(amname,
  false)` check in `CreateOpFamily` — rather than silently registering a
  method-OID-0 family) and the schema to a namespace OID exactly like the
  `"operator"` case, then calls `RegisterUserOperatorFamily`.
- **Planner**: no change needed — `CompatNoopStmt` was already in the
  DDL-passthrough case list from `CREATE OPERATOR`'s own landing.

### pg_dump mechanics (no goopg "dump" code — real PG's own logic)

`getOpfamilies` runs a flat `SELECT tableoid, oid, opfmethod, opfname,
opfnamespace, opfowner FROM pg_opfamily` — no join. `dumpOpfamily` then
issues two *separate* queries per family: `pg_amop`/`pg_amproc` joined
against `pg_depend` filtered to `refclassid = pg_opfamily AND refobjid =
<this family's oid>`. Since goopg registers no `pg_amop`/`pg_amproc`/
`pg_depend` rows for a user family (no `ALTER OPERATOR FAMILY ... ADD`
support yet), both queries return 0 rows for every family goopg creates, so
`dumpOpfamily`'s `ALTER OPERATOR FAMILY ... ADD ...;` block is correctly
never emitted — only the unconditional `CREATE OPERATOR FAMILY %s USING
%s;` line. The `ALTER OPERATOR FAMILY ... OWNER TO` line comes from the
archiver's generic per-TOC-entry owner mechanism (`_getObjectDescription`
building `"OPERATOR FAMILY name USING method"` from the `DROP` statement
text), the same mechanism slice 406's `CREATE OPERATOR` assertion already
depends on — no additional goopg-side code needed.

### Scope / limitations (deferred — see the ledger)

- `ALTER OPERATOR FAMILY ... ADD (OPERATOR/FUNCTION entries)` is not
  implemented — a family can be created but never populated with loose
  members. No current fixture needs it (upstream's `op_family` fixture is
  the bare/empty form; the fuller `op_class`/`op_class_custom` fixtures
  bundle their operators directly into `CREATE OPERATOR CLASS ... AS ...`,
  which is a separate, still-minimal stub — see below).
- `CREATE OPERATOR CLASS` itself remains the pre-existing minimal stub from
  M0097-0027 (`execCreateOpClass` only tracks the `FUNCTION 2` hash-extended
  support function and a schema association for `DROP SCHEMA CASCADE`
  detail text) — it does **not** populate `pg_opclass`, and its parser
  (`parseCreateOpClassTail`) only recognizes `OPERATOR`/`FUNCTION` entries
  in the `AS` list (not e.g. a bare `STORAGE type` entry). Full `CREATE
  OPERATOR CLASS` round-trip (and the `op_class_custom` ordering fixture
  that combines a custom operator + opclass + range-type `subtype_opclass`
  reference) is a materially larger follow-up, tracked separately.
- Family ownership (`Owner`) defaults to the bootstrap superuser exactly
  like `UserOperator.Owner` — goopg's DDL surface has no per-session
  creating-role tracking for this statement family.

### Gates

`TestParseCreateOperatorFamily`/`TestParseCreateOperatorFamilyUnqualified`/
`TestParseCreateOperatorClassStillWorks` (parser, `op_compat_test.go`);
`TestCreateOperatorFamily`/`TestCreateOperatorFamilyIdempotent`/
`TestCreateOperatorFamilyUnknownMethod` (executor, `create_operator_test.go`);
new DU-002 slice 408 assertions in `TestPort_PgDumpConnectionSetup`
(`internal/testport/pgdump_connsetup_test.go`) — byte-identical CREATE +
OWNER TO lines verified against a live PG 18.3 instance, plus a negative
assertion that no spurious `ALTER OPERATOR FAMILY ... ADD` line appears for
an empty family. `go build ./...` clean; `go vet`
parser/catalog/executor/planner clean; `internal/parser`+`internal/catalog`+
`internal/executor`+`internal/planner` suites PASS; `gofmt -l` flags only
the same pre-existing go1.25/1.26-drift files as loop #33 (verified via
`git stash`); TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit
hook.

## Loop #35: `CREATE OPERATOR CLASS` populates a real `pg_opclass` row (DU-002 slice 409)

Extends `CREATE OPERATOR CLASS` beyond the M0097-0027 minimal stub (which
only tracked the `FUNCTION 2` hash-extended support function + a schema
association for `DROP SCHEMA CASCADE` detail text) to populate a full
`pg_opclass` row — the loop #34 ledger row's resume point (b), bounded to
upstream's own `op_class_empty` `002_pg_dump.pl` fixture: `FOR TYPE bigint
USING btree FAMILY dump_test.op_family AS STORAGE bigint` — a class with a
`STORAGE` clause but **no** `OPERATOR`/`FUNCTION` members, so `dumpOpclass`'s
`pg_amop`/`pg_amproc`-via-`pg_depend` member queries (still unmodeled — see
below) correctly return 0 rows and only the class's own attributes need to
round-trip.

- **Parser** (`internal/parser/ddl.go`'s `parseCreateOpClassTail`): the name
  is now parsed via `p.parseObjectName()` (schema-qualified, matching `CREATE
  OPERATOR FAMILY`'s own parsing — previously a single unqualified token,
  the pre-existing limitation loop #34 explicitly left alone). `DEFAULT` is
  now captured (`CreateOpClassStmt.IsDefault`) instead of merely skipped;
  the access method name after `USING` is now captured
  (`CreateOpClassStmt.Method`) instead of discarded; a new optional `FAMILY
  family_name` clause is recognized right after the method (`"family"` is
  not in goopg's keyword map, so it arrives as a bare `TokenIdent`, matching
  the existing `"type"`/`"operator"` contextual-keyword pattern in this same
  function) and captured as `FamilySchema`/`FamilyName`; the `AS`-list
  scanner gains a `STORAGE type` entry alongside the pre-existing
  `OPERATOR`/`FUNCTION` recognition, captured as `CreateOpClassStmt.
  StorageType`. `OPERATOR`/`FUNCTION` entries themselves are still
  accepted-and-discarded beyond the pre-existing `FUNCTION 2` hash-func
  capture — see Scope/limitations.
- **Catalog** (`internal/catalog/catalog.go`): new `UserOperatorClass`
  struct (`OID`/`Name`/`NamespaceOID`/`Owner`/`Method`/`FamilyOID`/
  `InTypeOID`/`IsDefault`/`KeyTypeOID`, with `OwnerOrDefault`/
  `NamespaceOIDOrDefault` mirroring `UserOperatorFamily`'s) +
  `RegisterUserOperatorClass`/`DropUserOperatorClass`/
  `ListUserOperatorClasses`, keyed `"<schema>.<name>/<method-oid>"`
  (mirrors `userOpFamilyKey` — PG scopes opclass-name uniqueness per
  namespace+access-method too). New `LookupUserOperatorFamily` (by
  schema/name/method) resolves an explicit `FAMILY` clause.
  `pg_opclass.VirtualRows` now renders `ListUserOperatorClasses()` instead
  of the hardcoded `nil`.
- **Executor** (`internal/executor/operators_ddl.go`, `execCreateOpClass`):
  resolves the access method via `AccessMethodOIDByName` (`42704` if
  unrecognized, mirroring `CREATE OPERATOR FAMILY`'s own check), the schema
  to a namespace OID, and `ForType`/`StorageType` via `catalog.
  TypeNameToOID`. Family resolution: an explicit `FAMILY` clause must name
  an already-`CREATE`d family (`LookupUserOperatorFamily`; `42704` if not
  found, mirroring `opfamilycmds.c`'s "operator family ... does not exist"
  check); an omitted `FAMILY` clause auto-creates an anonymous family
  sharing the class's own schema+name (PG's `DefineOpClass`,
  `opclasscmds.c` — `opcfamily` is `NOT NULL`, so every class needs a valid
  family even when the user never wrote `CREATE OPERATOR FAMILY`), reusing
  `RegisterUserOperatorFamily` (idempotent by key, so a second class in the
  same auto-family reuses the same family row). `DROP OPERATOR CLASS` now
  also calls `DropUserOperatorClass` alongside the pre-existing
  `RemoveOpClass` (best-effort — schema defaults to `"public"` if the DROP
  statement omitted a qualifier, matching this function's existing
  convention) so a create-then-drop-then-dump sequence doesn't leave a
  ghost `pg_opclass` row.

### pg_dump mechanics (no goopg "dump" code — real PG's own logic)

`getOpclasses` runs a flat `SELECT tableoid, oid, opcmethod, opcname,
opcnamespace, opcowner FROM pg_opclass` — no join (the family/intype/
default/keytype detail is fetched per-class by `dumpOpclass` itself via a
`LEFT JOIN pg_opfamily`/`pg_namespace` keyed on `opcfamily`, plus
`opcintype`/`opckeytype` cast to `regtype`). `dumpOpclass` renders `CREATE
OPERATOR CLASS name\n    [DEFAULT ]FOR TYPE intype USING amname[ FAMILY
ns.famname] AS\n    `, then a `STORAGE keytype` clause **only if**
`opckeytype != InvalidOid` (PG's `regtype` output for `InvalidOid` is the
literal string `"-"`), then the `pg_amop`/`pg_amproc`-via-`pg_depend`
member queries (both 0 rows for every goopg-created class today), and
finally — only if nothing was printed after `AS` at all — a dummy `STORAGE
opcintype` filler so the statement isn't syntactically empty (not
exercised by this slice, since `op_class_empty` always supplies an explicit
`STORAGE`). The trailing `ALTER OPERATOR CLASS ... OWNER TO` line comes
from the same generic archiver owner mechanism as every other object in
this file — no goopg-side rendering code needed.

### Scope / limitations (deferred — see the ledger)

- `OPERATOR`/`FUNCTION` entries in a class's `AS` list are still not tied to
  a `pg_amop`/`pg_amproc` member store — a class declaring real members
  (upstream's `op_class`/`op_class_custom` fixtures) would currently dump
  with only its `STORAGE`-or-dummy clause, silently dropping every member.
  This needs the same `pg_amop`/`pg_amproc` + synthetic `pg_depend` member
  store that `ALTER OPERATOR FAMILY ... ADD` (loop #34's deferral) also
  needs — the two are naturally one follow-up, since `dumpOpclass` and
  `dumpOpfamily` both read the identical `pg_amop`/`pg_amproc`-via-
  `pg_depend` shape, only filtered by a different `refobjid` (class vs
  family).
- `op_class_custom` additionally needs a range-type `subtype_opclass`
  binding (`CREATE TYPE ... AS RANGE (subtype_opclass = ...)`), unrelated
  to the member-store gap above.
- Regtype rendering of `KeyTypeOID == 0` (`InvalidOid`, PG's own "no
  explicit STORAGE" sentinel) as the literal `"-"` is unverified — every
  fixture this loop added supplies an explicit `STORAGE`, so `KeyTypeOID`
  is always non-zero. A class *without* `STORAGE` (relying on PG's own
  dummy-filler path) is untested.
- Class ownership (`Owner`) defaults to the bootstrap superuser exactly like
  `UserOperatorFamily.Owner` — no per-session creating-role tracking.

### Gates

`TestParseCreateOperatorClassFullShape`/`TestParseCreateOperatorClassDefaultKeyword`
(parser, `op_compat_test.go`, alongside the pre-existing
`TestParseCreateOperatorClassStillWorks` regression guard);
`TestCreateOperatorClassPopulatesOpclassRow`/
`TestCreateOperatorClassAutoCreatesFamily`/
`TestCreateOperatorClassUnknownFamily` (executor, `create_operator_test.go`);
new DU-002 slice 409 assertion in `TestPort_PgDumpConnectionSetup`
(`internal/testport/pgdump_connsetup_test.go`) — the exact `CREATE OPERATOR
CLASS public.op_class_empty\n    FOR TYPE bigint USING btree FAMILY
public.op_family AS\n    STORAGE bigint;` shape verified byte-for-byte
against a live, freshly-built PG 18.3 instance (`postgres/local_install`) in
this loop. `go build ./...` clean; `go vet`
parser/catalog/executor/planner/testport clean; `internal/parser`+
`internal/catalog`+`internal/executor`+`internal/planner` suites PASS;
`gofmt -l` flags only the same pre-existing go1.25/1.26-drift files as loop
#34 (verified via `git stash`); TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench
smoke = pre-commit hook.

## Still open under M0119-0004

`regoper`/`regoperator` OID→name resolution (no column is typed `regoper`
yet, so no observable gap — and, per loop #36's investigation below, blocked
on a builtin-operator catalog that doesn't exist at all); `ALTER OPERATOR
FAMILY ... ADD` and `CREATE OPERATOR CLASS`'s own `OPERATOR`/`FUNCTION`
member entries (a single combined `pg_amop`/`pg_amproc`+`pg_depend`
member-store follow-up, per loop #35's scope note above); the
`op_class_custom` ordering fixture (range-type `subtype_opclass` binding);
`KeyTypeOID == 0` → `"-"` regtype rendering (untested); further pg_dump
002–010 catalog parity slices.

**2026-07-01 (loop #36):** while scoping the member-store follow-up above,
found and closed one of its two prerequisites — see
`0119-0004-regproc-oid-name-resolution.md`'s "Follow-up: regprocedure
argument-type-list rendering" section. `dumpOpclass`/`dumpOpfamily` cast
`pg_amproc.amproc::pg_catalog.regprocedure`, which goopg previously rendered
identically to `::regproc` (bare name, no argument-type list) — now fixed.
The SECOND prerequisite, confirmed still fully open: goopg has no
builtin-operator catalog at all (`pg_operator`'s `VirtualRows` renders only
user-defined operators), so `regoper`/`regoperator` resolution for a BUILTIN
operator has no data source — this blocks `amopopr::pg_catalog.regoperator`
and, transitively, byte-exact upstream `op_family`/`op_class` fixture
porting (which reference real builtin cross-type btree operators, not just
user-defined ones). The member store itself should be scoped to
user-defined operators/functions first (fully resolvable today) with the
builtin-operator-catalog gap ledgered separately, per the 2026-07-01 slice
410 deferral-ledger row.

**2026-07-01 (loop #37, slice 411): the pg_amop/pg_amproc + pg_depend member
store landed**, scoped to user-defined operators/functions as planned above.

- Parser: `CreateOpClassStmt.Members []OpClassMember` — `parseCreateOpClassTail`
  (`internal/parser/ddl.go`) now captures every `OPERATOR`/`FUNCTION` AS-list
  entry (strategy/support number, operator/function name, explicit operand/
  arg types when given) instead of discarding everything but `FUNCTION 2`.
  Reused the existing `parseOperatorRefName` helper (built for CREATE
  OPERATOR's own COMMUTATOR/NEGATOR clauses, slice 407) for the
  `OPERATOR(schema.op)`-qualified-or-bare operator-name grammar. `opclass_purpose`
  (`FOR SEARCH` / `FOR ORDER BY family_name`) is parsed-and-discarded — the
  referenced sort opfamily is not resolved/stored (amopsortfamily stays 0,
  amoppurpose always `'s'`); this is a new, narrow deferral (no fixture in
  scope needs an ordering operator).
- Catalog: new `AmOpMember`/`AmProcMember` (append-only slices on `InMemory`,
  `RegisterAmOpMember`/`RegisterAmProcMember`/`ListAmOpMembers`/
  `ListAmProcMembers`); `pg_amop`/`pg_amproc`'s `VirtualRows` now render them
  (previously hardcoded `nil`); `dependVirtualRows` emits the two pg_depend
  rows per member that `dumpOpclass`'s own query needs (`classid=pg_amop/
  pg_amproc → refclassid=pg_operator/pg_proc`, `deptype='n'`; `→
  refclassid=pg_opclass`, `deptype='i'`) mirroring `storeOperators`/
  `storeProcedures` (`opclasscmds.c`) for a class-attributed ("hard")
  reference — confirmed against `getDependencies`' own SQL that `'i'` rows
  are correctly excluded from its generic pg_amop→pg_opfamily dependency
  rewrite (would otherwise be a self-dependency). `DropUserOperatorClass` now
  cascades member cleanup by `ClassOID` so a create-then-drop-then-dump
  sequence leaves no ghost rows.
- Executor: `execCreateOpClass` resolves each member via
  `resolveOpClassOperator`/`resolveOpClassFunction`
  (`internal/executor/operators_ddl.go`) — `LookupUserOperator`/new
  `LookupUserOperatorByName` (schema+name only, lowest-OID tiebreak, used
  when the AS-list entry has no explicit operand types) for OPERATOR
  entries; `Routines().LookupByName` then `catalog.LookupBuiltinProc` for
  FUNCTION entries. An entry naming an unresolvable builtin operator is
  silently dropped — same as every entry's behavior before this loop, now
  correctly scoped to just the unresolvable subset instead of everything.
  Unspecified lefttype/righttype default from the resolved operator's own
  `oprleft`/`oprright` or the resolved function's own first-arg type
  (mirroring `assignOperTypes`/`assignProcTypes`, `opclasscmds.c`).
- **Two adjacent bugs found and fixed via live-PG diff (not part of the
  member-store feature itself, but directly exposed by it):**
  1. `::regtype` of `InvalidOid` (0) rendered the literal `"0"` instead of
     PG's `"-"` (`regtypeout`, `regproc.c`) — a class with real members and
     no `STORAGE` clause dumped a spurious `STORAGE 0` line (the same
     `KeyTypeOID == 0` gap this doc's slice-409 section flagged as
     "unverified" is now CONFIRMED and fixed). Guard added to both the
     `KindString`-numeric-parse and `KindInt` branches of the `regtype`
     `CastExpr` in `internal/executor/expr.go`, mirroring the existing
     `regclass` InvalidOid guard next to it.
  2. `::regoperator`/`::regoper` had no resolution at all (recognized as a
     type name, zero `CastExpr` handling) — `dumpOpclass`'s
     `amopopr::pg_catalog.regoperator` cast rendered a bare numeric OID.
     New `catalog.(*InMemory).RegoperatorName` (mirrors `RegprocedureName`,
     `format_operator`/`regoperatorout` shape `name(lefttype,righttype)`,
     `"NONE"` for a missing/unary side) wired into a new `regoper`/
     `regoperator` `CastExpr` branch; `InvalidOid` renders as the literal
     `"0"` (regoperatorout/regoperout's own sentinel — NOT `"-"` like
     regproc/regtype, confirmed from `postgres/src/backend/utils/adt/
     regproc.c`).
  3. Added `btint4cmp` (pg_proc.dat oid 351) to the hand-curated
     `builtinProcsByName` set so a `FUNCTION 1` opclass entry can reference a
     semantically valid (integer-returning) btree comparator — real PG's
     `opclasscmds.c` rejects a boolean-returning proc for strategy 1 with
     "ordering comparison functions must return integer", caught via the
     live-PG diff below.
- **Verified end-to-end against a freshly-built, live PG 18.3 instance**
  (`postgres/local_install`) in this loop:
  `CREATE OPERATOR public.~=~ (FUNCTION = int4eq, LEFTARG = int4, RIGHTARG =
  int4); CREATE OPERATOR FAMILY public.op_family USING btree; CREATE
  OPERATOR CLASS public.op_class FOR TYPE int4 USING btree FAMILY
  public.op_family AS OPERATOR 1 ~=~ (int4, int4), FUNCTION 1
  btint4cmp(int4, int4);` dumps byte-identical on both engines EXCEPT one
  known, deferred gap (below): PG prefixes the OPERATOR entry's operator
  name with its schema (`public.~=~(integer,integer)`), goopg does not
  (`~=~(integer,integer)`).
- **New deferral (confirmed via the live-PG diff above, not previously
  tracked): `RegoperatorName`/`RegprocedureName` (slice 410) never
  schema-qualify.** Root cause: `pg_dump`'s connection ALWAYS runs with
  `search_path=''` (`ALWAYS_SECURE_SEARCH_PATH_SQL`,
  `postgres/src/include/common/connect.h`, applied at `connectdb.c:228`), so
  `format_operator`/`format_procedure`'s own visibility check
  (`OperatorIsVisible`/`FunctionIsVisible`) NEVER finds an unqualified name
  visible for pg_dump's session specifically — every reference is
  force-qualified, even for an object in `public`. goopg tracks no
  search_path/visibility model at all, so both renderers always emit an
  unqualified name. This is a real, confirmed gap (not merely a theoretical
  edge case) but a proper fix needs schema/search_path visibility
  infrastructure that doesn't exist yet — out of scope for this slice.
  Resume point: add an `InMemory` OID→schema-name lookup (no such helper
  exists today; `allSchemasLocked()` returns `{oid, name}` pairs internally
  but isn't exported) and, since pg_dump's connection is provably ALWAYS
  unqualified-nothing-visible, the simplest correct fix is to make
  `RegoperatorName`/`RegprocedureName` unconditionally prepend
  `schema.` — no visibility-check logic needed, because pg_dump's own
  connection never has anything visible. Apply to both renderers together
  (same architectural cause, `internal/catalog/catalog.go`).
- Tests: `TestCreateOperatorClassMembersPopulateAmopAmproc`,
  `TestCreateOperatorClassMemberUnresolvableBuiltinDropped`,
  `TestDropOperatorClassRemovesMembers` (executor,
  `create_operator_test.go`). Gates: `go build ./...`/`go vet` clean;
  `internal/parser`+`internal/catalog`+`internal/executor` suites PASS;
  `TestPort_PgDumpConnectionSetup` PASS (no pg_dump regression); live PG
  18.3 diff above; TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke =
  pre-commit hook.

## Loop #38 — `regoperator`/`regprocedure` schema-qualification (DU-002 slice 412)

Closes the loop #37 row's deferral (c): `RegoperatorName`/`RegprocedureName`
never schema-qualified, but pg_dump's connection always runs `search_path=''`
(`ALWAYS_SECURE_SEARCH_PATH_SQL`, `postgres/src/include/common/connect.h`,
applied in `setup_connection` at `pg_dump.c:1379`), so `format_operator`/
`format_procedure`'s own visibility check (`OperatorIsVisible`/
`ProcedureIsVisible`, `regproc.c`) never finds an unqualified reference
visible for that session — every non-`pg_catalog` object comes back
schema-qualified.

**Deviation from loop #37's proposed resume point.** That row suggested the
"simplest correct fix" was to make both renderers *unconditionally* prepend
`schema.`, reasoning that pg_dump's search_path is always empty so nothing is
ever visible. Re-reading `format_operator_extended`/`format_procedure_extended`
(`regproc.c`) before implementing surfaced a case that plan would have gotten
wrong: PG's own doc-stated rule is that **`pg_catalog` is always implicitly
searched regardless of `search_path`'s content** (functions.sgml, "the system
catalog schema is always searched, whether it is mentioned in the path or
not"). A builtin function/operator therefore stays *unqualified* in a real
pg_dump output even though `search_path=''` — e.g. `dumpOpclass`'s own
`FUNCTION 1 btint4cmp(int4, int4)` entry (a builtin) renders as
`btint4cmp(integer,integer)`, not `pg_catalog.btint4cmp(integer,integer)`.
Unconditional qualification would have force-qualified builtins too, which
is a real (if narrow) accuracy regression the byte-diff below would not have
caught on its own (the fixture's only FUNCTION entry happens to be builtin,
so this loop's live-diff exercises exactly that case).

- New `catalog.RegprocedureNameAndSchema`/`(*InMemory).RegoperatorNameAndSchema`
  (returning `(schema, sig, ok)` alongside the existing bare-name functions,
  which now delegate to them) resolve the object's schema: `"pg_catalog"` for
  a builtin `regprocedure` (matches `pgProcArgTypeNamesByOID`'s builtin
  branch), the `CREATE FUNCTION` routine's declared `Schema` (default
  `"public"`) for a user-defined one, and the operator's `NamespaceOID`
  (default `"public"`) for `regoperator` — with a special case for
  `PublicNamespaceOID` (2200), since `NewInMemory`'s `schemas` map aliases
  both `"public"` and `"pg_toast"` to that same OID (a pre-existing
  simplified-model quirk) and a plain `SchemaNameForOID` reverse lookup would
  nondeterministically pick either name depending on Go's randomized map
  iteration order — caught by an intermittent test failure
  (`pg_toast.~=~(integer,integer)` instead of `public.~=~(...)`) when the
  full `internal/executor` suite ran rather than the new test in isolation.
- New `executor.regObjectSchemaVisible(ctx, schema)` mirrors
  `OperatorIsVisible`/`ProcedureIsVisible`'s actual rule: `pg_catalog` (or an
  empty schema) is always visible; every other schema must appear in
  `searchPathSchemas(ctx)` (the session's already-existing effective
  search_path resolver, reused as-is — `currentSchemaFromSearchPath`'s own
  helper). Wired into both `regprocedure`/`regoperator` `CastExpr` branches
  in `internal/executor/expr.go`: schema-qualify only when
  `!regObjectSchemaVisible(ctx, schema)`.
- `appendTypedCellText`'s direct-column-typed `regprocedure` rendering
  (`internal/server/dispatch.go`) is intentionally left unchanged: it has no
  per-session context to consult (`s.cfg.Catalog` is server-global, not
  connection-scoped), and `dumpOpclass`/`dumpOpfamily` never reach it anyway
  — both always cast explicitly (`amproc::pg_catalog.regprocedure`,
  confirmed by grepping `pg_dump.c`), which routes through the `CastExpr`
  path above, not the raw-column-type renderer.
- **Verified against a freshly-built, live PG 18.3 instance**
  (`postgres/local_install`) in this loop: the exact loop #37 fixture
  (`CREATE OPERATOR public.~=~ ...; CREATE OPERATOR CLASS public.op_class
  ... AS OPERATOR 1 ~=~ (int4, int4), FUNCTION 1 btint4cmp(int4, int4);`)
  now dumps byte-identical on both engines — `OPERATOR 1
  public.~=~(integer,integer)` (qualified, user-defined) alongside
  `FUNCTION 1 (integer, integer) btint4cmp(integer,integer)` (bare,
  builtin), closing the last known gap loop #37 left open.
- Tests: `TestRegprocedureRegoperatorSchemaQualification` (executor,
  new `regoperator_schema_qualify_test.go`) — pins bare rendering under the
  default `"$user", public` search_path, qualified rendering under
  `search_path=''` for a user-defined function/operator, and bare rendering
  for a builtin function under *both*. Gates: `go build ./...`/`go vet`
  clean; `internal/catalog`+`internal/executor`+`internal/parser`+
  `internal/server`+`internal/planner` suites PASS (run repeatedly to
  confirm the map-iteration flake above is gone); `TestPort_PgDumpConnectionSetup`
  PASS; `gofmt -l` flags only pre-existing go1.25/1.26 comment-smart-quote
  drift on the touched files (confirmed the diff hunks don't overlap this
  loop's edited line ranges); live PG 18.3 diff above; TPC-H spotcheck
  Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook.
- Deferred (unchanged, ledger rows carried forward): `FOR ORDER BY`
  sort-family resolution (loop #37 (a)); the builtin-operator catalog (loop
  #36/#37 (b)) remains the single largest blocker for a realistic
  `op_family`/`op_class` fixture.

## Loop #39 — curated builtin-operator catalog + `op_class` opckeytype fix (DU-002 slice 413)

Closes the loop #36/#37 "builtin-operator catalog" finding for the exact
upstream `op_family`/`op_class` fixture: `postgres/src/bin/pg_dump/t/002_pg_dump.pl`
`'CREATE OPERATOR CLASS dump_test.op_class'` — a bigint btree opclass whose
`OPERATOR` entries name real **built-in** operators (`<`, `<=`, `=`, `>=`, `>`
over `int8`), not user-defined ones, which `resolveOpClassOperator`/
`RegoperatorNameAndSchema` had no way to resolve.

**Scope decision:** rather than a full `pg_operator.dat` port (~799 rows;
`cmd/gen-pg-operator-data`/`internal/initdb/pg_operator_seed_data.go` already
carries that full breadth for the PG18-standby heap-fidelity bootstrap path,
but that data lives in `internal/initdb`, which `internal/catalog`/
`internal/executor` cannot import — mirrors the `pg_proc` split between
`internal/initdb/pg_proc_seed_data.go` (heap bootstrap) and
`internal/catalog/pg_proc_names_generated.go` (leaf-package name index)), this
loop follows the established `builtinProcsByName` pattern instead: a small
hand-curated `internal/catalog` map holding only the operators an actual
fixture references, extended incrementally. A full generated
`pg_operator_names_generated.go` leaf copy (mirroring `-names` mode of
`cmd/gen-pg-proc-data`) remains the eventual fuller fix — ledgered separately,
not attempted here.

- `catalog.BuiltinOperator` + `builtinOperatorsByKey` (keyed by `name +
  "/" + leftOID + "/" + rightOID`, synonym-proof since both sides are
  resolved via `TypeNameToOID` before keying) + `builtinOperatorsByOID`
  (reverse index) + `LookupBuiltinOperator`/`LookupBuiltinOperatorByOID`
  (`internal/catalog/catalog.go`, beside `builtinProcsByName`). Curated set:
  the 5 int8 btree comparison strategies (`pg_operator.dat` oids
  410/412/413/414/415) plus `btint8cmp` (oid 842, added to
  `builtinProcsByName`) for `FUNCTION 1`.
- `resolveOpClassOperator`'s typed branch (`internal/executor/operators_ddl.go`)
  now falls back to `catalog.LookupBuiltinOperator` when
  `LookupUserOperator` misses (mirrors `resolveOpClassFunction`'s existing
  `LookupBuiltinProc` fallback). The untyped (bare-name) branch is
  unchanged — `TestCreateOperatorClassMemberUnresolvableBuiltinDropped`
  still documents that scope boundary (no fixture needs it).
- `catalog.RegoperatorNameAndSchema` falls back to
  `LookupBuiltinOperatorByOID` (schema always `"pg_catalog"`) when
  `LookupUserOperatorByOID` misses; the bare-name `regoper` `CastExpr`
  branch (`internal/executor/expr.go`) gets the same fallback.
- **A second, independent bug found via the live-PG-18.3 diff below:**
  `execCreateOpClass`'s `keyTypeOID` (`internal/executor/operators_ddl.go`)
  never reset to `InvalidOid` when the `STORAGE` clause names the same type
  as the class's own `FOR TYPE` — but real PG does exactly that
  (`opclasscmds.c` `DefineOpClass`: `if (storageoid == typeoid) storageoid =
  InvalidOid`). For a class with real `OPERATOR`/`FUNCTION` members (like
  `op_class`, which also declares the redundant `STORAGE bigint`), this
  meant goopg's dump spuriously emitted a leading `STORAGE bigint ,` line
  that real PG's `dumpOpclass` never prints (`opckeytype::regtype` reads
  `"-"`, so the `if (strcmp(opckeytype, "-") != 0)` branch never fires).
  For a class with **no** members (`op_class_empty`), the fix is a no-op on
  observable output: `opckeytype` still renders `"-"` server-side, but
  pg_dump's own client-side "dummy STORAGE clause" fallback
  (`pg_dump.c`, fired whenever the `AS` list would otherwise render empty,
  since `... AS ;` isn't valid SQL) re-adds `STORAGE bigint` using
  `opcintype` — reproducing the identical text through a different branch.
  `TestCreateOperatorClassPopulatesOpclassRow`'s `opckeytype` assertion
  (previously pinning the wrong "20", i.e. the redundant STORAGE clause was
  NOT reset) is corrected to `"0"` with a comment explaining why
  `op_class_empty`'s dump text is unaffected.
- **Verified against a freshly-built, live PG 18.3 instance**
  (`postgres/local_install`): the exact upstream `op_class`/`op_class_empty`
  fixture pair (schema renamed `dump_test` → `public`) dumps byte-identical
  on both engines — `op_class` has no `STORAGE` line and ends `FUNCTION 1
  (bigint, bigint) btint8cmp(bigint,bigint);` (matching the upstream test's
  own regex comment: "it's correct that btint8sortsupport and btequalimage
  are NOT included here" — goopg reproduces this because those two builtins
  are deliberately not curated in `builtinProcsByName`, so
  `resolveOpClassFunction` silently drops them, same mechanism as every
  other unresolvable-builtin entry); `op_class_empty` keeps `STORAGE
  bigint;` via the dummy-fallback path described above.
- Tests: `TestLookupBuiltinOperator`/`TestLookupBuiltinOperatorByOID`/
  `TestRegoperatorNameAndSchemaBuiltinFallback` (`internal/catalog/builtin_operator_test.go`);
  `TestCreateOperatorClassMembersResolveBuiltinOperators` (executor,
  `create_operator_test.go`) ports the upstream `op_class` fixture verbatim
  and asserts all 5 `pg_amop` rows, the single `pg_amproc` row, and
  `RegoperatorNameAndSchema`'s builtin-fallback rendering;
  `TestCreateOperatorClassPopulatesOpclassRow`'s `opckeytype` assertion
  corrected (see above).
- Gates: `go build ./...`/`go vet` (catalog/executor/parser/server/planner/
  testport) clean; those 5 packages' suites PASS; `TestPort_PgDumpConnectionSetup`
  PASS; live PG 18.3 diff above; TPC-H spotcheck + pgbench smoke run before
  commit.
- Deferred (ledger row appended, unchanged in kind, narrower in scope): the
  builtin-operator catalog is still only a 5-row curated slice (int8 btree
  comparison strategies), not a full `pg_operator.dat` port — the next
  fixture referencing a different builtin operator (e.g. a GiST/GIN/hash
  opclass, or any non-int8 btree class) will need its own curated addition
  until a generated leaf-package index exists. `FOR ORDER BY` sort-family
  resolution remains open and unexercised by any fixture in scope.

## Loop #40 — `FOR ORDER BY` sort-family resolution (DU-002 slice 414)

Closes the loop #37/#39 "FOR ORDER BY sort-family resolution" deferral.
`parseCreateOpClassTail`'s `opclass_purpose` branch (`internal/parser/ddl.go`)
now captures `FOR ORDER BY family_name` onto the member
(`OpClassMember.SortFamilySchema`/`SortFamilyName`, `internal/parser/ast.go`)
instead of parsing-and-discarding it.

- `catalog.AmOpMember` gains `SortFamilyOID` (`internal/catalog/catalog.go`);
  `pg_amop.VirtualRows` derives `amoppurpose` from it (`'o'` AMOP_ORDER when
  non-zero, else `'s'` AMOP_SEARCH — mirrors opclasscmds.c's `oppurpose =
  OidIsValid(op->sortfamily) ? AMOP_ORDER : AMOP_SEARCH`) and renders the
  real `amopsortfamily` OID instead of a hardcoded `"0"`. `dependVirtualRows`
  emits the extra NORMAL pg_depend row on the sort family that
  `storeOperators` also records ("A search operator also needs a dep on the
  referenced opfamily").
- `registerOpClassMembers` (`internal/executor/operators_ddl.go`) resolves a
  `FOR ORDER BY` entry's family **against the btree access method
  unconditionally** — confirmed by re-reading `opclasscmds.c`:
  `sortfamilyOid = get_opfamily_oid(BTREE_AM_OID, item->order_family,
  false)` is NOT parameterized by the containing class's own method. Missing
  family errors 42704 (mirrors `get_opfamily_oid`'s own `missing_ok=false`
  ereport).
- **Significant discovery from live-diffing PG 18.3 with this exact
  fixture:** `FOR ORDER BY` is legal on essentially no access method except
  GiST/SP-GiST. Real PG's `assignOperTypes` checks `amroutine->amcanorderbyop`
  right after the sortfamily lookup, and only `gist.c`/`spgutils.c` set that
  flag `true` in the whole in-tree AM-handler set (btree/hash/gin/brin all
  leave it at the zero-value default `false`). goopg had no such
  capability-flag concept at all — a plain-btree `FOR ORDER BY` would
  previously have silently "succeeded" with wrong catalog contents. New
  check in `registerOpClassMembers` rejects a resolved-sortfamily member
  whose containing class's method isn't gist(783)/spgist(4000), erroring
  `42P17` (`ERRCODE_INVALID_OBJECT_DEFINITION`) with PG's own exact message
  text (`access method "%s" does not support ordering operators`).
- **Verified byte-identical against a freshly-built, live PG 18.3 instance**
  (`postgres/local_install`) for both paths:
  - Accept path: a `USING gist` class, `OPERATOR 1 ... FOR ORDER BY
    sort_family` — raw `pg_amop` row's `amoppurpose`/`amopsortfamily`/
    `amoplefttype`/`amoprighttype`/`amopstrategy`/`amopopr`/`amopmethod`
    columns byte-identical between engines.
  - Reject path: `USING btree` + `FOR ORDER BY` → identical `ERROR: access
    method "btree" does not support ordering operators` text on both
    engines.
- Tests: `TestParseCreateOperatorClassForOrderBy` (parser);
  `TestCreateOperatorClassForOrderBySortFamily`/
  `TestCreateOperatorClassForOrderByUnknownFamilyErrors`/
  `TestCreateOperatorClassForOrderByRejectsNonOrderingAM` (executor).
- Gates: `go build ./...`/`go vet` clean; `internal/catalog`+
  `internal/executor`+`internal/parser`+`internal/planner`+`internal/server`
  suites PASS; `TestPort_PgDumpConnectionSetup` PASS; live PG 18.3 diff
  above; gofmt drift confirmed pre-existing via `git stash`; TPC-H spotcheck
  Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook.
- **New, larger discovery, deferred (ledger row appended):** real PG's
  `gistadjustmembers` (the only `amadjustmembers` override in the in-tree AM
  set) forces EVERY GiST opclass OPERATOR member — not just `FOR ORDER BY`
  ones — to a *soft* (`DEPENDENCY_AUTO`, deptype `'a'`) dependency on the
  containing OPFAMILY, never a *hard* (`DEPENDENCY_INTERNAL`, deptype `'i'`)
  dependency on the OPCLASS. Confirmed via the live PG 18.3 diff: real
  `pg_depend` shows `refclassid=pg_opfamily` (deptype `'a'`) for both the
  operator→op_family and operator→sort_family edges, never
  `refclassid=pg_opclass`. This means `dumpOpclass`'s own OPERATOR query
  (filters `refclassid = pg_opclass`) can never find a GiST/SP-GiST
  opclass's AS-list OPERATOR entries in real PG — they're dumped via a
  separate `ALTER OPERATOR FAMILY ... ADD OPERATOR ...` statement instead
  (`dumpOpfamily`'s loose-member query, not implemented — loop #34's still-open
  resume point (a)). goopg's `registerOpClassMembers`/`dependVirtualRows`
  unconditionally emit the hard opclass-level `'n'`+`'i'` pair for every AM
  (verified correct for btree in loop #37/#39) — for GiST/SP-GiST this is a
  confirmed, pre-existing divergence (not new to this loop — it already
  applied to any GiST/SP-GiST opclass member, `FOR ORDER BY` or not; this
  loop's diffing is simply what surfaced it, since `FOR ORDER BY` is the one
  clause that forces a GiST/SP-GiST fixture into existence). See the ledger
  row for the full resume plan (a per-AM `amadjustmembers`-equivalent policy
  table, plus the still-separately-scoped `ALTER OPERATOR FAMILY ... ADD`
  statement).

## Loop #41 — `ALTER OPERATOR FAMILY ... ADD` loose members (DU-002 slice 415)

Closes the loop #34 row's original resume point (a): `ALTER OPERATOR FAMILY
name USING method ADD entry [, entry ...]` (opclasscmds.c `AlterOpFamilyAdd`)
attaches OPERATOR/FUNCTION members directly to an existing family with no
owning opclass — previously the whole `ALTER OPERATOR FAMILY|CLASS` tail
(ADD included) fell into `parseAlter`'s generic consume-and-succeed no-op
stub.

- New parser `parseAlterOpFamilyTail` (`internal/parser/ddl.go`) recognizes
  the `USING method ADD ...` shape and produces a real `AlterOpFamilyAddStmt`
  (`internal/parser/ast.go`), reusing `OpClassMember` for entries. The `DROP`
  form (and any other/unrecognized tail) still falls back to the pre-existing
  `AlterTableStmt` no-op stub — deferred, ledgered, since removing a member
  also means undoing its pg_depend rows.
- Unlike `CREATE OPERATOR CLASS`'s own `AS` list, an `ADD`'d `OPERATOR` entry
  **requires** an explicit `(lefttype, righttype)` pair — new
  `OpClassMember.HasExplicitArgTypes` records whether the parenthesized form
  was present; the omitted case parses fine (PG's grammar allows it) but
  `execAlterOpFamilyAdd` raises PG's own syntax error text ("operator
  argument types must be specified in ALTER OPERATOR FAMILY") at DDL-exec
  time, matching PG's own phase for that check (`opclasscmds.c`, not
  `gram.y`).
- New executor `execAlterOpFamilyAdd` (`internal/executor/operators_ddl.go`)
  resolves the family (42704 if missing, mirrors `get_opfamily_oid`'s own
  `missing_ok=false`) and calls the existing `registerOpClassMembers` with
  `classOID=0` (the "loose member" sentinel) and a new `isAdd` flag. `isAdd`
  also gates a duplicate-member check (42710, mirrors `storeOperators`'/
  `storeProcedures`' own isAdd conflict check — `CREATE OPERATOR CLASS`'s AS
  list performs no such check).
- **Dependency-strength switch** (`dependVirtualRows`,
  `internal/catalog/catalog.go`): a loose member (`ClassOID == 0`) gets the
  *soft* dependency shape real PG's `storeOperators`/`storeProcedures` use
  for `ALTER ADD` — AUTO (`'a'`) on the operator/function itself, and AUTO on
  the **family** (`refclassid=pg_opfamily`, not `pg_opclass`) — instead of
  the hard NORMAL+INTERNAL pair a class-attributed member gets. This is
  exactly the shape `dumpOpfamily`'s own loose-member query (filtered on
  `refclassid=pg_opfamily`) needs to find and re-emit these members as a
  follow-up `ALTER OPERATOR FAMILY ... ADD` statement.
- **Verified byte-identical against a freshly-built, live PG 18.3 instance**
  (`postgres/local_install`): `CREATE OPERATOR FAMILY public.op_family_loose
  USING btree` + `ALTER OPERATOR FAMILY public.op_family_loose USING btree
  ADD OPERATOR 1 < (bigint, bigint), OPERATOR 3 = (bigint, bigint), FUNCTION
  1 (bigint, bigint) btint8cmp(bigint, bigint)` (the loop #39 curated int8
  builtin-operator set) dumps identically on both engines, including a real
  pg_dump formatting quirk — a trailing space before each `OPERATOR` entry's
  comma (`OPERATOR 1 <(bigint,bigint) ,`) that does not appear after a
  `FUNCTION` entry.
- Tests: `TestParseAlterOperatorFamilyAdd`/
  `TestParseAlterOperatorFamilyAddRequiresArgTypes`/
  `TestParseAlterOperatorFamilyDropStillNoop` (parser);
  `TestAlterOperatorFamilyAddRegistersLooseMembers`/
  `TestAlterOperatorFamilyAddRequiresExplicitArgTypes`/
  `TestAlterOperatorFamilyAddDuplicateMemberErrors`/
  `TestAlterOperatorFamilyAddUnknownFamilyErrors` (executor); new DU-002
  slice 415 fixture + assertions in `TestPort_PgDumpConnectionSetup`
  (`internal/testport`) using a separate `op_family_loose` family so the
  slice 408 fixture's "family stays empty" negative assertion is unaffected.
- Gates: `go build ./...`/`go vet ./...` clean; `internal/parser`+
  `internal/executor`+`internal/catalog`+`internal/planner`+`internal/server`
  suites PASS; `TestPort_PgDumpConnectionSetup` PASS; live PG 18.3 diff
  above; TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook.
- Still open (unrelated, pre-existing): the `DROP` form of `ALTER OPERATOR
  FAMILY`; the per-AM `amadjustmembers` dependency-strength policy for
  gist/spgist opclass members (loop #40's discovery — a materially larger,
  separately-scoped follow-up); the `op_class_custom` ordering fixture
  (range-type `subtype_opclass` binding); the builtin-operator catalog
  remains a 6-row curated slice (int8 5-strategy set + `btint8cmp`, unchanged
  this loop).

## Loop #43 — `ALTER OPERATOR FAMILY ... DROP` (DU-002 slice 416)

Closes the loop #41 row's resume point (1): the `DROP` form of `ALTER
OPERATOR FAMILY`, direct sibling of loop #41's `ADD` form.

- New parser `AlterOpFamilyDropStmt` (`internal/parser/ast.go`) +
  `parseAlterOpFamilyDropTail` (`internal/parser/ddl.go`), reached from
  `parseAlterOpFamilyTail` when the tail is `DROP` rather than `ADD`. The
  grammar is narrower than `ADD`'s `opclass_item` (`gram.y opclass_drop`):
  each entry is `OPERATOR strategynum '(' type [',' type] ')'` or `FUNCTION
  supportnum '(' type [',' type] ')'` — the strategy/support number and the
  parenthesized type list are both **mandatory**, and there is no
  operator/function name at all (`processTypesSpec`'s single-type shorthand
  defaults `righttype = lefttype`, reused via `OpClassMember.LeftType`/
  `RightType`). Reuses `OpClassMember` (as `ADD` does), leaving
  `Name`/`Schema`/`SortFamily*` at their zero values since `DROP` has none of
  those.
- New executor `execAlterOpFamilyDrop` (`internal/executor/operators_ddl.go`)
  resolves the family (42704 if missing, same `LookupUserOperatorFamily` call
  `execAlterOpFamilyAdd` uses) and, for each entry, resolves `LeftType`/
  `RightType` to OIDs via `catalog.TypeNameToOID` and calls new
  `catalog.RemoveAmOpMember`/`RemoveAmProcMember` keyed on
  `(familyOID, leftType, rightType, strategy-or-procnum)` — the same
  4-column key PG's `dropOperators`/`dropProcedures` look up via
  `GetSysCacheOid4(AMOPSTRATEGY/AMPROCNUM, ...)`. A missing member raises
  42704 (`undefined_object`), text-shaped after `dropOperators`'/
  `dropProcedures`' own `ereport` (mirrors the existing duplicate-member
  message's convention of using the raw parsed type-name string, not
  `format_type_be` output, and the family's own bare name rather than a
  schema-qualified one — same simplification `execAlterOpFamilyAdd`'s 42710
  message already uses, not a new gap).
- **No new pg_depend plumbing needed**: `dependVirtualRows` computes every
  pg_amop/pg_amproc dependency row live from `c.amOpMembers`/
  `c.amProcMembers` on each read (loop #41), so removing an entry from either
  slice makes its pg_depend rows disappear automatically — `RemoveAmOpMember`/
  `RemoveAmProcMember` only need to delete the catalog row itself.
- **Verified against a freshly-built, live PG 18.3 instance**
  (`postgres/local_install`, ad hoc `dump_test.op_family_loose` family from
  loop #41's own fixture SQL): `ALTER OPERATOR FAMILY ... DROP OPERATOR 1
  (bigint, bigint), FUNCTION 1 (bigint, bigint)` removes exactly those two
  rows from `pg_amop`/`pg_amproc` on real PG; a repeat `DROP` of the same
  entry raises `operator 1(bigint,bigint) does not exist in operator family
  "dump_test.op_family_loose"` (42704); `DROP OPERATOR 3 (bigint)` (single
  type) correctly matches the `(bigint,bigint)` row via the
  righttype-defaults-to-lefttype shorthand. goopg's behavior and error text
  shape match (module the pre-existing unqualified-family-name
  simplification noted above).
- Tests: `TestParseAlterOperatorFamilyDrop`/
  `TestParseAlterOperatorFamilyDropRequiresParens` (parser, replacing the
  stale loop #41 `TestParseAlterOperatorFamilyDropStillNoop` no-op-stub
  pin — DROP is a real statement now); `TestAlterOperatorFamilyDropRemovesLooseMember`/
  `TestAlterOperatorFamilyDropMissingMemberErrors`/
  `TestAlterOperatorFamilyDropUnknownFamilyErrors` (executor).
- Gates: `go build ./...`/`go vet ./...` clean; `internal/parser`+
  `internal/executor`+`internal/catalog`+`internal/planner`+`internal/server`
  suites PASS; `TestPort_PgDumpConnectionSetup` PASS (loop #41's ADD fixture
  unaffected — DROP has no fixture of its own since pg_dump itself never
  emits this form, it only appears in hand-written DDL); live PG 18.3 diff
  above; TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook;
  `gofmt -l` flags only the same pre-existing go1.25/1.26-drift files as
  every prior loop in this chain (verified via `git stash`).
- Deferred (ledger row appended): dropping a **class-attributed** (hard,
  `ClassOID != 0`) member is not specially handled — goopg removes the row
  unconditionally like a loose member, whereas real PG's `performDeletion`
  would see the member's `INTERNAL` dependency on its owning opclass and
  either cascade-drop the whole opclass or raise a restrict error depending
  on drop mode. No fixture in scope exercises this (pg_dump-driven round
  trips never issue `ALTER OPERATOR FAMILY ... DROP` at all); the per-AM
  `amadjustmembers` policy and the builtin-operator-catalog gap remain open
  and unchanged from loop #41.

## Loop #45 — per-AM `amadjustmembers` dependency-strength policy (gist/spgist)

Closes the loop #40 ledger row's own resume point: "port `gistadjustmembers`'s
policy ... for `USING gist`/`USING spgist` opclasses, OPERATOR members always
get a soft/family-level dependency ... and only a hand-curated subset of
FUNCTION members ... keep the hard opclass-level dependency" — flagged in the
loop #44 working-set carry as "the largest structural gap ... needed for any
real GiST/SP-GiST opclass to round-trip through pg_dump."

### The bug

`dependVirtualRows` (`internal/catalog/catalog.go`) decided every
`AmOpMember`/`AmProcMember`'s pg_depend hardness purely from `ClassOID == 0`
(loose ALTER-ADD'd member → soft/AUTO on the opfamily; class-attributed member
→ hard/INTERNAL on the opclass), with no per-access-method distinction. Real
PG's `DefineOpClass`/`AlterOpFamilyAdd` (`opclasscmds.c`) call the AM's own
`amadjustmembers` routine (`amRoutine->amadjustmembers`) right before
`storeOperators`/`storeProcedures`, and GiST/SP-GiST are the two in-tree AMs
whose override (`gistvalidate.c gistadjustmembers`, `spgvalidate.c
spgadjustmembers`) forces every OPERATOR member's dependency to soft/family
**regardless of class-attribution** ("Operator members of a GiST opfamily
should never have hard dependencies, since their connection to the opfamily
depends only on what the support functions think, and that can be altered"),
and forces every **optional** FUNCTION member (anything not in the AM's own
required-support-proc list) to soft/family too, while the AM's required
support procs stay hard-on-class. goopg's blanket "class-attributed is always
hard" rule missed this entirely — a `USING gist`/`USING spgist`
`CREATE OPERATOR CLASS ... AS OPERATOR ...` member always rendered
`refclassid=pg_opclass`/`INTERNAL`, which is what real PG's `dumpOpclass`
query (`pg_dump.c`, `JOIN pg_depend ... WHERE refclassid = pg_opclass`) needs
to include the member in the class's own inline `AS` list — but real PG,
having marked the same member `refclassid=pg_opfamily`/`AUTO`, does **not**
include it there at all; it only shows up via `dumpOpfamily`'s separate
"loose member" query (`WHERE refclassid = pg_opfamily`), rendered as an
`ALTER OPERATOR FAMILY ... ADD` statement. Confirmed by re-reading
`opclasscmds.c` `DefineOpClass`/`AlterOpFamilyAdd` (both call
`amroutine->amadjustmembers` unconditionally before storing) and
`gistvalidate.c`/`spgvalidate.c`'s own verbatim comments.

### The fix

- `AmProcMember` (`internal/catalog/catalog.go`) gains a `Method uint32`
  field (mirroring `AmOpMember.Method`, already present since slice 411) so
  `dependVirtualRows` can look up the owning AM per FUNCTION row without a
  family-OID indirection; `RegisterAmProcMember` takes a new `method`
  parameter, threaded from `registerOpClassMembers`'s existing `methodOID`
  local (`internal/executor/operators_ddl.go`) — its one call site already
  had the value in scope.
- Two small per-AM policy tables plus two predicate functions, all in
  `internal/catalog/catalog.go`, right after `RemoveAmProcMember`:
  - `amGISTMethodOID`(783)/`amSPGistMethodOID`(4000) constants (mirroring
    `AccessMethodOIDByName`'s own OID values).
  - `gistRequiredSupportProcs`/`spgistRequiredSupportProcs`: the amprocnum
    sets `gistadjustmembers`/`spgadjustmembers` keep hard
    (`GIST_CONSISTENT/UNION/PENALTY/PICKSPLIT/EQUAL_PROC` = {1,2,5,6,7};
    `SPGIST_CONFIG/CHOOSE/PICKSPLIT/INNER_CONSISTENT/LEAF_CONSISTENT_PROC` =
    {1,2,3,4,5} — both read directly off `gist.h`/`spgist.h`).
  - `amForcesSoftOperatorDependency(methodOID)`: true for gist/spgist,
    unconditionally (matches the function loop's unconditional
    `op->ref_is_hard = false` for every operator).
  - `amForcesSoftFunctionDependency(methodOID, procNum)`: true for gist/spgist
    when `procNum` is **not** in the AM's required set; false for every other
    AM (preserving btree/hash/gin/brin's existing hard-on-class default,
    unexamined — no fixture in scope exercises a non-gist/spgist class-
    attributed optional-function case, and PG's own btree/hash
    `amadjustmembers` implement a materially different, cross-type-driven
    rule that is out of this bounded slice's scope — see "Still open" below).
- `dependVirtualRows`'s two member loops (amOpMembers/amProcMembers) extend
  their existing `if m.ClassOID == 0 { ...soft... }` guard to
  `if m.ClassOID == 0 || amForcesSoft{Operator,Function}Dependency(...)`.
  Everything downstream (the operator/function-ref deptype, the
  class-or-family refclassid/refobjid/deptype, the sort-family row) already
  branched on the same three-variable state (`refDeptype`,
  `classOrFamilyRefclassid`/`Refobjid`/`Deptype`) introduced in slice
  411/415, so no other code changed.

### Verification

- **Regression correction, not just addition**: this fix flips
  `TestCreateOperatorClassForOrderBySortFamily` (loop #40, slice 414) — a
  `USING gist` class's `FOR ORDER BY` OPERATOR member is class-attributed
  (`ClassOID != 0`), so the pre-fix code asserted its sort-family pg_depend
  row as `NORMAL` ('n'). That was always wrong per real PG (confirmed by
  `DefineOpClass` calling `amadjustmembers` unconditionally, before
  `storeOperators` — see the "The bug" section); corrected to `AUTO` ('a')
  with a comment explaining why, rather than silently left stale.
- New `TestCreateOperatorClassGistMembersGetSoftDependencies`
  (`internal/executor/create_operator_test.go`): a `USING gist` class with
  one OPERATOR member, one required FUNCTION (amprocnum 1), one optional
  FUNCTION (amprocnum 3) — asserts the OPERATOR's dependency is soft/family
  (even class-attributed), the required FUNCTION's is hard/class, and the
  optional FUNCTION's is soft/family, plus a negative assertion that the
  required FUNCTION does NOT also carry a soft/family row.
- **Live PG 18.3 end-to-end proof** (not just a pg_depend row shape check):
  built two side-by-side servers — goopg (`tmp/perf-optimize`, port 5533)
  and a genuinely fresh `initdb`-created real PostgreSQL 18.3
  (`postgres/local_install`, port 5534) — ran the identical DDL (`CREATE
  OPERATOR ~=~`, `CREATE OPERATOR FAMILY ... USING gist`, `CREATE OPERATOR
  CLASS ... USING gist FAMILY ... AS OPERATOR 1 ..., FUNCTION 1 ..., FUNCTION
  3 ...`) against both, then ran the **same real `pg_dump` binary** against
  each. Output content is byte-identical on the load-bearing lines (only
  object-dump *ordering* differs — pg_dump's own topological sort, a
  pre-existing, unrelated, separately-scoped gap — see "Still open" below):
  - `CREATE OPERATOR CLASS ... AS FUNCTION 1 (integer, integer)
    int4eq(integer,integer);` — **only** the required FUNCTION 1 stays in
    the class's own inline AS-list on BOTH engines.
  - `ALTER OPERATOR FAMILY public.gist_family USING gist ADD OPERATOR 1
    public.~=~(integer,integer) , FUNCTION 3 (integer, integer)
    int4eq(integer,integer);` — the OPERATOR and the optional FUNCTION 3
    both round-trip through the *existing* `execAlterOpFamilyAdd` (slice 415)
    machinery automatically, with **no new dump-side code**: real pg_dump's
    `dumpOpfamily` query (`WHERE refclassid = pg_opfamily AND amopfamily =
    ...`) is unconditional on how a row was created — it only reads
    pg_depend content, so correcting the pg_depend row's `refclassid` was
    sufficient to make this fixture's OPERATOR/optional-FUNCTION visible via
    the loose-member path this milestone had already built.
- Gates: `go build ./...`/`go vet` on `internal/catalog`+`internal/executor`
  clean; targeted `TestCreateOperatorClass*`/`TestAlterOperatorFamily*` PASS;
  full `internal/catalog`+`internal/executor`+`internal/parser`+
  `internal/planner`+`internal/server` suites PASS;
  `TestPort_PgDumpConnectionSetup` PASS (no automated fixture in that suite
  exercises a gist/spgist opclass yet — the loop #39/#40 gist verification
  was always a manual live-PG diff, not a permanent fixture — so this fix
  has zero interaction with the existing regression corpus); live PG 18.3
  end-to-end diff above; TPC-H spotcheck + pgbench smoke (pre-commit hook).

### Still open (unrelated, pre-existing, NOT this loop's scope)

- **Dump *ordering***: goopg's real-pg_dump output orders `CREATE OPERATOR
  CLASS` before `CREATE OPERATOR FAMILY`/`ALTER OPERATOR FAMILY ADD`; real PG
  orders the family/ALTER pair before the class. Both are internally valid
  (an `ALTER OPERATOR FAMILY` targeting an already-dumped family doesn't need
  the class to exist first), and pg_dump's own object-ordering is a
  dependency-graph topological sort goopg does not replicate — a separate,
  materially larger gap (goopg's dump path has no general
  dependency-ordering pass at all, per the M0119-0004 catalog-view-parity
  umbrella), not specific to opclasses.
- **btree/hash's own `amadjustmembers`** (`nbtvalidate.c`/`hashvalidate.c`)
  implement a different, cross-type-driven rule — a same-type operator/
  required-proc ties to the opclass (hard) only if one exists, an explicit
  cross-type one is always loose/soft regardless of class-attribution. goopg
  keeps its pre-existing "class-attributed is always hard" default for these
  two AMs unconditionally (no cross-type distinction at all). No fixture in
  scope forces this (every existing btree opclass fixture is same-type);
  ledgered as a distinct, smaller follow-up if one ever does.
- The builtin-operator catalog remains the loop #39 6-row curated slice; the
  `op_class_custom` range-type ordering fixture remains unexercised; `ALTER
  OPERATOR FAMILY ... DROP`'s class-attributed cascade/restrict semantics
  (loop #43) remain unimplemented.

## Loop #54 — `CREATE FOREIGN TABLE ... SERVER ... OPTIONS (...)` round-trip (DU-002 slice 417)

Closes the pre-existing `pg_foreign_table` gap noted since M0110-0001: the
view was hardcoded to `func() [][]string { return nil }` and
`parseCreateForeignTableTail` discarded the entire `SERVER ...
OPTIONS (...)` suffix, so a `CREATE FOREIGN TABLE` always dumped as a plain
`CREATE TABLE` (relkind stayed `'r'`, no `pg_foreign_table` row). This is
independent of the OPERATOR/OPCLASS/OPFAMILY chain above (same DU-002
umbrella milestone, unrelated object type); the slice counter is shared
across the whole M0119-0004 catalog-view-parity effort, not per-subsystem.

- Parser: `CreateTableStmt` gains `ForeignServer`/`ForeignOptions`
  (`internal/parser/ast.go`). `parseCreateForeignTableTail`
  (`internal/parser/ddl.go`) now captures `SERVER name` (via
  `p.parseObjectName`) and an optional table-level `OPTIONS (...)` (via the
  pre-existing `scanFDWOptionsList`, already used by `CREATE SERVER`/`CREATE
  USER MAPPING`) instead of skipping to `;`. `parseColumnDef` also accepts
  (and discards) a per-column `OPTIONS (...)` clause — real PG's column-level
  FDW options land in `pg_attribute.attfdwoptions`, which goopg does not
  model, and no in-scope fixture asserts it — so the column list of a
  multi-option foreign table still parses cleanly without silently
  mis-tokenizing the trailing `OPTIONS (...)` as part of the next column.
- Catalog: `Table` gains `ForeignServerName`/`ForeignOptions`
  (`internal/catalog/catalog.go`). `registerSystemTables`'s `pg_class`
  relkind derivation gains an `else if t.ForeignServerName != ""` branch
  (`relkind = "f"`), alongside the existing view/matview/partitioned-table
  branches. `pg_foreign_table.VirtualRows` (previously hardcoded empty) now
  scans `c.tables`, emitting `(ftrelid, ftserver, ftoptions)` for every table
  with a non-empty `ForeignServerName` — `ftserver` resolves through the
  existing `foreignServers` map/`ForeignServerOID` (same registry `CREATE
  SERVER` populates), `ftoptions` reuses the existing `optionsArrayLiteral`
  helper (same text-array encoding `pg_foreign_server.srvoptions` already
  uses).
- Executor: `execCreateTable` (`internal/executor/operators_ddl.go`) validates
  `s.ForeignServer` against the catalog's foreign-server registry *before*
  calling `CreateTable`, raising `42704` (`undefined_object`) if the named
  server doesn't exist — mirrors real PG's `DefineRelation` calling
  `GetForeignServerByName(..., false)` ahead of `heap_create_with_catalog`,
  so a bad `SERVER` name never leaves a half-created relation behind (goopg
  has no transactional catalog rollback for a partially-created table, so
  checking first is load-bearing, not just a nicety). On success, stores
  `ForeignServerName`/`ForeignOptions` onto the new `catalog.Table`.
- Test: extended `TestPort_PgDumpConnectionSetup`
  (`internal/testport/pgdump_connsetup_test.go`) — reuses the `goopg_srv`
  foreign server from the pre-existing slice-376 fixture (no new `CREATE
  SERVER` needed) to `CREATE FOREIGN TABLE public.goopg_ftable (c1 int
  options (column_name 'col1')) SERVER goopg_srv OPTIONS (schema_name
  'x1')`, then asserts pg_dump emits the exact upstream
  `002_pg_dump.pl`-shaped block (`CREATE FOREIGN TABLE public.goopg_ftable
  (\n    c1 integer\n)\nSERVER goopg_srv\nOPTIONS (\n    schema_name
  'x1'\n);`) plus a negative assertion that no plain `CREATE TABLE
  public.goopg_ftable (` line leaked out (the regression signature if
  `relkind` fell back to `'r'`, e.g. from a silently-failed server lookup).
- **Verified against real `pg_dump` 18.3** via the existing
  `TestPort_PgDumpConnectionSetup` harness (spawns the real client binary
  against a live goopg server) — not a hand-rolled string comparison against
  a mocked catalog.
- Gates: `go build ./...`/`go vet ./...` clean; `internal/parser`+
  `internal/catalog`+`internal/executor` suites PASS (`-count=1`);
  `TestPort_PgDumpConnectionSetup` PASS; TPC-H spotcheck Q12=2/Q13=33 PASS;
  pgbench smoke = pre-commit hook.
- Deferred (ledger row appended): the per-column `OPTIONS (...)` clause is
  parsed and discarded — `pg_attribute.attfdwoptions` is not modelled, so a
  fixture asserting column-level FDW options would fail today. `CREATE
  SERVER`'s `srvtype`/`srvversion` and a real FDW-handler execution path
  (actually reading from a remote source at query time) remain entirely out
  of scope for goopg's compat-only foreign-table support — `ForeignServer`/
  `ForeignOptions` exist purely so DDL+`pg_dump` round-trip; no query ever
  executes against a real remote source.

## Loop #55 — per-column `OPTIONS (...)` round-trip (`pg_attribute.attfdwoptions`, DU-002 slice 418)

Closes the loop #54 resume point: the `goopg_ftable` fixture already declared
`c1 int OPTIONS (column_name 'col1')`, but `parseColumnDef`'s `OPTIONS (...)`
case called `p.scanFDWOptionsList()` purely to consume the tokens and
discarded the result, so `pg_attribute.attfdwoptions` stayed NULL and
pg_dump's per-column FDW-options query (`pg_options_to_table(attfdwoptions)`)
always returned zero rows.

- Parser: `ColumnDef` gains `FDWOptions []string` (`internal/parser/ast.go`);
  `parseColumnDef`'s `OPTIONS` case (`internal/parser/ddl.go`) now assigns
  `col.FDWOptions = p.scanFDWOptionsList()` instead of discarding it. The
  helper already normalizes to the on-disk `"name=value"` element form (used
  identically by `CREATE SERVER`/`CREATE USER MAPPING`/table-level
  `ForeignOptions`), so no new encoding logic was needed.
- Catalog: `Column` gains `FDWOptions []string` (`internal/catalog/catalog.go`),
  documented as the attfdwoptions analogue of the pre-existing `Options`
  (attoptions) field.
- Executor: both CREATE TABLE column-construction sites in
  `internal/executor/operators_ddl.go` (the `addCol` closure used by the
  `BodyOrder` loop, and the no-`BodyOrder` fallback loop) now copy
  `c.FDWOptions` onto the new `catalog.Column` field — mirroring how
  `Compression`/`Collation` are threaded at the same two sites.
- pg_attribute row builder: `buildUserPGAttributeRow`
  (`internal/executor/pg18_user_catalog_rows.go`) renders `col.FDWOptions`
  into the attfdwoptions text-array literal (`"{name=value,...}"`) using the
  same `strings.Join` pattern as the existing `attOptionsDatum` — was
  hardcoded `NullDatum`.
- Test: extended `TestPort_PgDumpConnectionSetup`
  (`internal/testport/pgdump_connsetup_test.go`) with a positive assertion
  that pg_dump emits `ALTER FOREIGN TABLE ONLY public.goopg_ftable ALTER
  COLUMN c1 OPTIONS (\n    column_name 'col1'\n);` (pg_dump.c's distinct
  "per-column fdw options" block, separate from the table-level
  `SERVER ... OPTIONS (...)` clause already covered by slice 417). New unit
  tests `TestParseColumnDefFDWOptions` (parser: FDWOptions capture + a sibling
  column with no OPTIONS clause stays empty) and
  `TestUserPGAttributeFDWOptionsOverride` (executor: NULL / one option / two
  options row-builder encoding, mirrors `TestUserPGAttributeOptionsOverride`).
- **Verified against real `pg_dump` 18.3** via `TestPort_PgDumpConnectionSetup`
  (spawns the real client binary against a live goopg server).
- Gates: `go build ./...`/`go vet ./...` clean; `internal/parser`+
  `internal/catalog`+`internal/executor` suites PASS (`-count=1`);
  `TestPort_PgDumpConnectionSetup` PASS; TPC-H spotcheck Q12=2/Q13=33 PASS;
  `gofmt -l` clean on every touched file; pgbench smoke = pre-commit hook.
- Deferred (ledger row appended): the `ALTER FOREIGN TABLE ONLY <t> ALTER
  COLUMN <c> OPTIONS (...)` statement this loop makes pg_dump newly *emit* is
  not itself parseable by goopg — `parseAlter` dispatches straight to
  `p.expectKeyword(KwTable)` with no `FOREIGN` lookahead, so a schema-only
  restore replay of a foreign table with column options against goopg itself
  would fail with a syntax error. No fixture in the current inventory
  exercises a goopg-to-goopg restore, so this was out of this loop's scope;
  `CREATE SERVER`'s `srvtype`/`srvversion` and real FDW-handler execution
  remain out of scope as noted at loop #54.

## Loop #56 — `ALTER FOREIGN TABLE ... ALTER COLUMN ... OPTIONS (...)` parsing+execution (DU-002 slice 419)

Closes the loop #55 resume point: pg_dump now *emits*
`ALTER FOREIGN TABLE ONLY <t> ALTER COLUMN <c> OPTIONS (...)` (slice 418), but
`parseAlter` dispatched straight to `p.expectKeyword(KwTable)` with no
`FOREIGN` lookahead, so goopg could not parse its own pg_dump output back —
the same failure a goopg-to-goopg schema restore of a foreign table with
column options would hit.

- Parser: `parseAlter` (`internal/parser/ddl.go`) gains a `FOREIGN` lookahead
  right before the `KwTable` expect, consumed only when `TABLE` follows —
  `ALTER FOREIGN DATA WRAPPER` (a structurally different, `TABLE`-keyword-less
  statement, already unmodeled) falls through unchanged to raise its
  pre-existing syntax error. Consuming `FOREIGN` here lets the rest of the
  function — `IF EXISTS`/`ONLY`/name/`ALTER COLUMN` — apply exactly as it
  does for a plain `ALTER TABLE`, so no new statement type or duplicate
  grammar was needed.
- Parser: the existing `ALTER COLUMN` block gained an `OPTIONS (...)` case
  (checked immediately after the column-name capture, before the `SET`/`TYPE`/
  `DROP` arms — PG's grammar has no leading `SET` for this form:
  `ALTER opt_column ColId alter_generic_options`, gram.y). It calls a new
  `scanAlterFDWOptionsList` (`internal/parser/ddl.go`), a verb-tagged sibling
  of `scanFDWOptionsList`: each entry is tagged `FDWOptionAdd`/`FDWOptionSet`/
  `FDWOptionDrop` from an optional `ADD`/`SET`/`DROP` keyword prefix, with a
  bare `name 'value'` defaulting to Add (mirrors PG's `DEFELEM_UNSPEC`-as-ADD
  rule in `alter_generic_option_elem`, gram.y). `DROP` takes no value, matching
  `DROP generic_option_name` (no `generic_option_arg`). New AST types
  `FDWOptionVerb`/`FDWOptionChange` and `AlterTableActionKind` constant
  `AlterTableAlterColumnOptions` (`internal/parser/ast.go`); `AlterTableAction`
  gains `FDWOptionChanges []FDWOptionChange`.
- Executor: new `execAlterTable` case for `AlterTableAlterColumnOptions`
  (`internal/executor/operators_ddl.go`) mirrors PG's
  `ATExecAlterColumnGenericOptions` (`tablecmds.c:15954`): rejects a non-foreign
  table with 42809 (`tbl.ForeignServerName == ""`, PG's "... is not a foreign
  table"), rejects an unknown column with 42703, then merges the change list
  onto `catalog.Column.FDWOptions` via a new `applyFDWOptionChanges` helper —
  a direct port of PG's `transformGenericOptions`
  (`postgres/src/backend/commands/foreigncmds.c:120-206`, read from the actual
  upstream source this loop, not from memory): `ADD`/bare errors 42710
  duplicate_object ("option \"%s\" provided more than once") if the option
  already exists; `SET`/`DROP` each error 42704 undefined_object ("option
  \"%s\" not found") if it does not; `SET` replaces the existing entry in
  place, `DROP` removes it, `ADD`/bare appends. Like the sibling
  `AlterTableSetCompression`/`AlterTableAlterColumnSet` cases, the in-memory
  mutation alone is invisible to pg_dump until flushed through the same
  delete-old-rows + `syncTableToCatalogHeap` re-sync path, which the new case
  follows identically.
- Test: `TestParseAlterForeignTableAlterColumnOptions`
  (`internal/parser/ddl_test.go`) — a single statement with `ONLY`, `ADD`,
  `SET`, `DROP`, and a bare (defaults-to-Add) entry, asserting the full
  verb-tagged `FDWOptionChanges` slice. `TestAlterForeignTableAlterColumnOptionsRoundtrip`
  and `TestAlterForeignTableAlterColumnOptionsErrors`
  (`internal/executor/operators_alter_foreign_table_options_test.go`) exercise
  the full `CREATE SERVER` → `CREATE FOREIGN TABLE` → `ALTER FOREIGN TABLE`
  sequence end-to-end via `newDDLFixture`/`runDDL` (the same harness
  `operators_alter_set_reloptions_test.go` uses for table-level reloptions),
  covering an ADD→SET+bare-ADD→DROP sequence and all four SQLSTATEs (42809,
  42703, 42710, 42704).
- Gates: `go build ./...`/`go vet ./...` clean; `internal/parser`+
  `internal/catalog`+`internal/executor` suites PASS (`-count=1`);
  `TestPort_PgDumpConnectionSetup` PASS; TPC-H spotcheck Q12=2/Q13=33 PASS;
  `gofmt -l` clean on every touched/new file (no diff overlaps the new code —
  the tool still flags the pre-existing go1.25-vs-go1.26.3 struct-comment
  alignment mismatch on unrelated lines, [[goopg_gofmt_version_mismatch_no_w]]);
  pgbench smoke = pre-commit hook.
- Deferred (ledger row appended): table-level `ALTER FOREIGN TABLE <t>
  OPTIONS (...)` (no `ALTER COLUMN` — PG's `AT_GenericOptions`/
  `ATExecGenericOptions`, setting `pg_foreign_table.ftoptions`) remains
  unmodeled; only the column-level form was in this loop's bounded scope.
  `ALTER FOREIGN DATA WRAPPER` remains entirely unparseable. No fixture pipes
  a literal `pg_dump | psql` goopg-to-goopg restore of a foreign table — the
  new executor tests construct the equivalent SQL directly.

## Loop #57 — `ALTER FOREIGN TABLE ... OPTIONS (...)` (table-level, DU-002 slice 420)

Closes the loop #56 resume point: real PG's `AT_GenericOptions` /
`ATExecGenericOptions` (`tablecmds.c:18663`, read from upstream source this
loop) is the table-level counterpart of the column-level
`AT_AlterColumnGenericOptions` case landed in loop #56 — a bare
`OPTIONS (...)` right after the table name (no `ALTER COLUMN`), merging onto
`pg_foreign_table.ftoptions` via the exact same `transformGenericOptions`
helper PG's column-level path calls. Confirmed via `pg_foreign_table`'s
`VirtualRows` (`internal/catalog/catalog.go`) that this catalog is fully
virtual — it reads `catalog.Table.ForeignOptions` live on every scan, unlike
`pg_attribute`'s heap-backed `attfdwoptions`, so unlike the column-level case
this one needs **no** delete-old-rows + `syncTableToCatalogHeap` re-sync step.

- Parser: `parseAlter` (`internal/parser/ddl.go`) gains a bare `OPTIONS (...)`
  check as a sibling of the pre-existing `ALTER COLUMN` block, checked right
  after `OWNER TO`/`RENAME`/`SET LOGGED`/`SET SCHEMA`/row-security/rule
  handling and before the `DROP CONSTRAINT`/`ALTER COLUMN` fall-through —
  reuses the existing `scanAlterFDWOptionsList` verb-tagged scanner unchanged
  (it already starts by consuming the `OPTIONS` token). New
  `AlterTableActionKind` constant `AlterTableSetForeignOptions`
  (`internal/parser/ast.go`); reuses the existing `AlterTableAction.
  FDWOptionChanges` field (doc comment widened to cover both uses).
- Executor: new `execAlterTable` case for `AlterTableSetForeignOptions`
  (`internal/executor/operators_ddl.go`), placed immediately after the
  `AlterTableAlterColumnOptions` case it mirrors: rejects a non-foreign table
  with 42809 (identical check/message to the column-level case — PG's
  `ATExecGenericOptions`/`ATSimplePermissions` path resolves the same "is not
  a foreign table" error for either generic-options form), then merges the
  change list onto `catalog.Table.ForeignOptions` via the existing
  `applyFDWOptionChanges` helper (already table-shape-agnostic — a plain
  `[]string`, no column indirection needed) — same 42710/42704 SQLSTATEs as
  the column-level case, since both route through one merge helper.
- Test: `TestParseAlterForeignTableSetForeignOptions`
  (`internal/parser/ddl_test.go`) mirrors the loop #56 parser test exactly
  (`ONLY`, `ADD`, `SET`, `DROP`, bare-defaults-to-Add) but asserts
  `AlterTableSetForeignOptions` with no `ColumnName`.
  `TestAlterForeignTableSetForeignOptionsRoundtrip` /
  `TestAlterForeignTableSetForeignOptionsErrors`
  (`internal/executor/operators_alter_foreign_table_options_test.go`) mirror
  the loop #56 executor tests, exercising `CREATE SERVER` → `CREATE FOREIGN
  TABLE ... OPTIONS (...)` → `ALTER FOREIGN TABLE ... OPTIONS (...)`
  end-to-end (ADD→SET+bare-ADD→DROP sequence, plus 42809/42710/42704).
- Gates: `go build ./...`/`go vet ./...` clean; `internal/parser`+
  `internal/catalog`+`internal/executor` suites PASS (`-count=1`);
  `TestPort_PgDumpConnectionSetup` PASS; TPC-H spotcheck Q12=2/Q13=33 PASS
  (see working_set.md for this loop's run); `gofmt -l` clean on every
  touched/new file (no diff overlaps the new code —
  [[goopg_gofmt_version_mismatch_no_w]] pre-existing drift only); pgbench
  smoke = pre-commit hook.
- Deferred (ledger row appended): pg_dump never actually *emits* this
  standalone statement for a foreign table it created — `dumpTableSchema`
  always inlines table-level options directly into the `CREATE FOREIGN TABLE
  ... SERVER ... OPTIONS (...)` clause (confirmed against the existing
  `goopg_ftable` fixture in `TestPort_PgDumpConnectionSetup`, which already
  round-trips `OPTIONS (schema_name 'x1')` inline at CREATE time), unlike the
  column-level form which PG's own pg_dump deliberately splits into a
  separate `ALTER ... ALTER COLUMN ... OPTIONS` statement. So this loop adds
  real ALTER-grammar parity for a user issuing the statement directly
  (post-creation option changes), but no pg_dump-driven fixture forces it —
  same caveat the loop #56 resume point already flagged. `ALTER FOREIGN DATA
  WRAPPER` remains entirely unparseable (unchanged from loop #56).

## Loop #58 — `ALTER FOREIGN DATA WRAPPER name OPTIONS (...)` parsing+execution

Closes the loop #57 resume point's first item: `ALTER FOREIGN DATA WRAPPER`
was "entirely unparseable" because it is a structurally distinct statement
from `ALTER [FOREIGN] TABLE` — PG's grammar (`gram.y`, `AlterFdwStmt`) has no
`TABLE` keyword and no relation-action list; it is `ALTER FOREIGN DATA
WRAPPER name [HANDLER handler_name|NO HANDLER] [VALIDATOR handler_name|NO
VALIDATOR]... [OPTIONS ( alter_generic_option_list )]`. Read upstream
`postgres/src/backend/parser/gram.y:5481-5499` (`AlterFdwStmt`) this loop to
confirm the exact production before implementing.

- Parser: `parseAlter` (`internal/parser/ddl.go`) gains a new branch
  recognising `FOREIGN` followed by the bare identifier `data` (not the
  `TABLE` keyword) — inserted *before* the pre-existing `ALTER FOREIGN TABLE`
  FOREIGN-consuming check, since that check only fires when `TABLE` follows
  and would otherwise let `ALTER FOREIGN DATA ...` fall through to
  `expectKeyword(KwTable)` and raise a raw syntax error (the loop #57
  ledger row's exact complaint). Mirrors `CREATE FOREIGN DATA WRAPPER`'s
  existing parse loop (`internal/parser/ddl.go` `CREATE` case): any token
  that is not the `OPTIONS` ident is skipped (so `HANDLER`/`NO HANDLER`/
  `VALIDATOR`/`NO VALIDATOR` and their function-name operands are silently
  discarded — goopg tracks no functions, same rationale CREATE's comment
  already gives), but unlike CREATE's flat `scanFDWOptionsList`, the `OPTIONS`
  clause here is scanned with the *verb-tagged* `scanAlterFDWOptionsList`
  (the same scanner `ALTER FOREIGN TABLE ... OPTIONS (...)` uses), because
  ALTER merges onto existing `fdwoptions` (PG's `transformGenericOptions`)
  rather than replacing them. Returns a `*CompatNoopStmt{Tag: "ALTER",
  ObjType: "foreign-data wrapper", ObjName: name, FDWOptionChanges: changes}`
  — a new `FDWOptionChanges []FDWOptionChange` field added to
  `CompatNoopStmt` (`internal/parser/ast.go`) alongside the pre-existing flat
  `Options []string` field CREATE uses, since ALTER's semantics differ enough
  that reusing `Options` would conflate "replace" with "merge".
- Catalog: new `(*InMemory).LookupForeignDataWrapper(name) (*ForeignDataWrapper,
  bool)` (`internal/catalog/catalog.go`) — a read-only lookup, distinct from
  the pre-existing `RegisterForeignDataWrapper` (create-or-fetch, used only by
  CREATE) because ALTER on a nonexistent FDW must error rather than silently
  create one.
- Executor: `execCompatNoop` (`internal/executor/operators_ddl.go`) gains an
  `s.Tag == "ALTER" && s.ObjType == "foreign-data wrapper"` branch, checked
  *before* the pre-existing unconditional `switch s.ObjType` block (which
  only ever runs for CREATE-tagged statements today, since ALTER is the first
  statement to reuse the `"foreign-data wrapper"` ObjType with a non-CREATE
  Tag): looks up the FDW via `LookupForeignDataWrapper`, 42704
  undefined_object if absent, else merges `s.FDWOptionChanges` onto
  `fdw.Options` via the existing `applyFDWOptionChanges` helper (same
  42710/42704 SQLSTATEs the ALTER FOREIGN TABLE OPTIONS cases already use for
  ADD-duplicate / SET-or-DROP-missing).
- Tests: `TestParseAlterForeignDataWrapperOptions`
  (`internal/parser/ddl_test.go`) — verb-tagged ADD/SET/DROP/bare parsing,
  plus a HANDLER-only form (no OPTIONS clause) parsing without error.
  `TestAlterForeignDataWrapperOptionsRoundtrip` /
  `TestAlterForeignDataWrapperOptionsErrors`
  (`internal/executor/operators_alter_foreign_data_wrapper_test.go`) mirror
  the ALTER FOREIGN TABLE OPTIONS executor tests one-for-one: `CREATE FOREIGN
  DATA WRAPPER ... OPTIONS (...)` → ADD → SET+bare-ADD → DROP sequence, plus
  the 42704 (nonexistent FDW)/42710/42704 error pins.
- Gates: `go build ./...`/`go vet ./...` clean; `internal/parser`+
  `internal/catalog`+`internal/executor` suites PASS (`-count=1`);
  `TestPort_PgDumpConnectionSetup` PASS; TPC-H spotcheck Q12=2/Q13=33 PASS;
  `gofmt -l` shows only pre-existing go1.25-vs-go1.26.3 struct-alignment
  drift, none overlapping this loop's new code
  ([[goopg_gofmt_version_mismatch_no_w]]); pgbench smoke = pre-commit hook.
- Deferred (ledger row appended): real PG's `pg_dump` never emits a
  standalone `ALTER FOREIGN DATA WRAPPER ... OPTIONS (...)` either — like the
  table-level foreign-table case, `dumpForeignDataWrapper` inlines the
  `OPTIONS (...)` clause directly into the `CREATE FOREIGN DATA WRAPPER`
  statement it emits (confirmed: no existing fixture pipes a `pg_dump | psql`
  goopg-to-goopg restore of an FDW, and `dumpForeignDataWrapper`'s only
  emitted form is the CREATE-time one). So this loop is real ALTER-grammar/
  executor parity for a user issuing the statement directly post-creation,
  not a pg_dump round-trip gap. The `HANDLER`/`VALIDATOR` clauses remain
  parsed-and-discarded (goopg tracks no pg_proc-backed FDW handler/validator
  functions at all — same limitation CREATE FOREIGN DATA WRAPPER already has,
  unchanged by this loop). No fixture yet exercises a goopg-to-goopg
  `pg_dump | psql` restore replay for ANY foreign-data-wrapper-family object
  (FDW, SERVER, FOREIGN TABLE, or USER MAPPING) — if one surfaces, that is
  the next concrete resume point for this whole DU-002 slice family.

## Loop #59 — `pg_publication.pubowner` populated (DU-002 slice 422)

The FDW-family thread (loops #54-58) ran dry: no fixture forces the two
remaining follow-ups (HANDLER/VALIDATOR functions, an FDW-family restore
replay), so this loop scoped a research pass to find the next divergence
instead. Live repro against real pg_dump 18.3 (`CREATE PUBLICATION pub1;`
then `pg_dump --schema-only`) surfaced: `pg_dump: error: role with OID 0 does
not exist` — **any dump of a database containing a publication crashed pg_dump
outright**, not a cosmetic per-object diff.

- Root cause: `pg_publication.VirtualRows`
  (`internal/initdb/replication_views.go`) hardcoded `pubowner` to `""` with
  the comment "roles aren't OID-stable yet"; `catalog.Publication`
  (`internal/catalog/pubsub.go`) had no owner field at all. Real pg_dump's
  `getPublications()` (`postgres/src/bin/pg_dump/pg_dump.c:4446-4495`)
  unconditionally selects `pubowner` and calls `getRoleName()` on it, which
  `pg_fatal()`s (`pg_dump.c:10531`) the instant the OID string doesn't parse
  to a known role — `atooid("")` is 0, and OID 0 is never a real role.
- Fix: `catalog.Publication` gains an `Owner uint32` field.
  `PubSub.CreatePublication` (`internal/catalog/pubsub.go`) now sets it to
  `10` (bootstrap superuser, "postgres") — the same hardcoded-owner
  convention already used by `CREATE CONVERSION` and `CREATE AGGREGATE`'s
  zero-value fallback (`OwnerOrDefault`) pending real per-session ownership
  tracking for DDL objects; no session/role-name plumbing existed at this
  call site to do better, and none of the sibling hardcoded-owner call sites
  do either. `pg_publication.VirtualRows` renders `fmt.Sprintf("%d",
  pub.Owner)` instead of `""` — `getRoleName`'s `atooid()` parses a decimal
  OID string, matching every other numeric-OID virtual column in this file.
- Test: extended `TestPort_PgDumpConnectionSetup`
  (`internal/testport/pgdump_connsetup_test.go`) with `CREATE PUBLICATION
  goopg_pub1 FOR ALL TABLES` in the setup phase and two new assertions —
  `CREATE PUBLICATION goopg_pub1 FOR ALL TABLES WITH (publish = 'insert,
  update, delete');` (goopg's `DefaultPublicationOptions` leaves `truncate`
  off, M0008-out-of-scope, so it's correctly absent from the WITH-list) and
  `ALTER PUBLICATION goopg_pub1 OWNER TO postgres;` (the archiver's generic
  owner-stamping path, `pg_backup_archiver.c` `_printTocEntry` /
  `_getObjectDescription`, confirmed it supports the `"PUBLICATION"` desc).
  The surrounding `res.ExitCode == 0` gate is itself part of the regression
  guard — before this fix pg_dump exited nonzero and the whole assertion
  block never ran.
- Gates: `go build ./...`/`go vet ./...` clean; `internal/catalog`+
  `internal/initdb`+`internal/executor` suites PASS; `TestPort_PgDump
  ConnectionSetup` PASS; TPC-H spotcheck Q12=2/Q13=33 PASS; `gofmt -l` shows
  only pre-existing go1.25-vs-go1.26.3 struct-alignment drift on an unrelated
  `var (...)` block in `pubsub.go`, none overlapping this loop's new fields
  ([[goopg_gofmt_version_mismatch_no_w]]); pgbench smoke = pre-commit hook.
- Deferred (ledger row appended): `pg_subscription.subowner`
  (`internal/initdb/replication_views.go`) has the exact same gap — hardcoded
  `""`, and real pg_dump's `getSubscriptions()` calls the same
  `getRoleName()` on it — but no fixture in this test currently issues
  `CREATE SUBSCRIPTION` (it requires a connection-string target and goopg's
  subscription DDL semantics differ enough from publication's that it's a
  separate, not-obviously-one-line investigation), so it's out of this
  loop's bounded scope. `catalog.Publication.Owner` is also always the
  bootstrap superuser (10) — a real `CREATE PUBLICATION` issued by a
  non-superuser role, or a subsequent `ALTER PUBLICATION ... OWNER TO`,
  isn't tracked (mirrors the same limitation every other hardcoded-owner
  object in this codebase already has).

## Loop #60 — `CREATE SUBSCRIPTION` round-trip + `is_superuser` GUC (DU-002 slice 423)

Picked up the loop #59 ledger row's resume point (`pg_subscription.subowner`).
Live repro (`CREATE SUBSCRIPTION sub1 CONNECTION '...' PUBLICATION pub1 WITH
(connect = false); pg_dump --schema-only`) surfaced that the CREATE
SUBSCRIPTION was silently absent from every dump — no crash, no warning in
stdout — which turned out to be **two separate, independently forcing bugs**,
not one:

1. **`is_superuser` never reflected the connecting role.** Real pg_dump's
   `getSubscriptions()` (`pg_dump.c:4972-5018`) gates the *entire* function on
   `is_superuser(fout)`, which reads the `is_superuser` value libpq captured
   from the startup `ParameterStatus` block — **not** a live `SHOW`. goopg
   registered `is_superuser` (`internal/config/defaults.go`) with `BootVal:
   "off"` and never overrode it per-connection, so pg_dump treated *every*
   connection, including the bootstrap `postgres` superuser, as unprivileged
   and silently skipped the whole subscription dump (`pg_log_warning` only
   fires to stderr, and only when a same-session non-superuser probe count is
   `>0` — it never even runs that probe here because the gate gets checked via
   the captured ParameterStatus, not a query).
2. **`pg_subscription.subdbid` never matched any live `pg_database.oid`.**
   `getSubscriptions()`'s `WHERE s.subdbid = (SELECT oid FROM pg_database
   WHERE datname = current_database())` needs an exact match or the row is
   silently excluded (not an error — a fidelity gap, since a fixture could
   pass its `res.ExitCode == 0` gate while still losing the object). `subdbid`
   was hardcoded `""`; the obvious fix (`catalog.InMemory.DBOID()`) turned out
   wrong too — `DBOID()` is the *storage* RelFileNode identity (`base/<oid>/`
   directory naming, see its doc comment), unrelated to the SQL-visible
   `pg_database.oid`, which `pgDatabase.VirtualRows`
   (`internal/catalog/catalog.go`) hardcodes to `catalog.FirstUserOID` (16384)
   for any non-template live database. Confirmed empirically: a fresh
   goopg-initialized cluster's `postgres` database showed `pg_database.oid =
   16384` while `DBOID()` returned `5` — two independently-tracked "database
   identity" numbers that happen to coincide only for physical-PG-standby
   scenarios `DBOID()` was actually built for.
3. Missing PG16/17 `pg_subscription` columns
   (`subpasswordrequired`/`subrunasowner`/`suborigin`/`subfailover`) — real
   pg_dump selects these by name for `remoteVersion >= 160000/170000` (PG18.3
   qualifies for both); an absent column would have been a harder, distinct
   "column does not exist" query failure once (1) and (2) were fixed, so all
   three were fixed together rather than discovering the third bug one loop
   later.

### Fix

- `internal/config/defaults.go`'s `is_superuser` GUC (`ContextInternal`,
  `FlagReport`) can't be reached by the normal `SessionRegistry.Set()` path
  (`Context < ContextSuset` is rejected by design — mirrors upstream's
  `PGC_INTERNAL` `set_config_option()` rejection). Added
  `SessionRegistry.SetInternal(name, value string)`
  (`internal/config/session.go`) as the trusted-backend-only bypass for this
  class of variable (doc comment warns against ever plumbing client input to
  it) — same shape as `Set()` minus the context gate, still firing the
  reportable-hook / global-onChange callbacks so `ParameterStatus` propagates.
- New `isSuperuserRoleName(roleName string) bool`
  (`internal/server/server.go`) mirrors the existing case-insensitive
  `"POSTGRES"` special case already used by every `SET ROLE`/`SET SESSION
  AUTHORIZATION` branch in `query.go` — goopg has no `CREATE ROLE ...
  SUPERUSER` attribute tracking at all (the whole privilege model is
  bootstrap-`postgres`-vs-everything-else, see `NonSuperuserRole`), so this
  reuses that convention rather than inventing a separate one.
  `serveConn`/`sendStartupReply`'s startup path now calls
  `sess.SetInternal("is_superuser", "on")` right after the existing
  `session_authorization` echo, for the connecting `user`.
- Kept the simple-query (`query.go`) and executor-dispatch (`dispatch.go`)
  `SET ROLE`/`SET SESSION AUTHORIZATION`/`RESET ROLE`/`RESET SESSION
  AUTHORIZATION` sites in sync: each site that flips
  `connTx.NonSuperuserRole` now also calls the new `setIsSuperuserGUC(sess,
  connTx.NonSuperuserRole == "")` helper (`query.go`) or inlines the
  equivalent `SetInternal` call in the executor's
  `ectx.SetSessionAuthorization` closure (`dispatch.go`) — a session that
  runs `SET ROLE` mid-connection and then re-checks its own privilege level
  (as some tools do) would otherwise see a stale value from connection
  startup. `dispatch_extended.go` has no analogous SET-role-tracking site
  today (the extended-protocol path doesn't implement `SET SESSION
  AUTHORIZATION` role-tracking at all yet), so there was nothing to mirror
  there — pre-existing gap, not a regression from this loop.
- `pg_subscription.VirtualRows`/`Columns`
  (`internal/initdb/replication_views.go`): `subdbid` now renders
  `catalog.FirstUserOID` (matching `pgDatabase.VirtualRows`'s convention
  instead of the unrelated `DBOID()`); `subowner` renders the new
  `catalog.Subscription.Owner` field (`internal/catalog/pubsub.go`, set to
  `10` by `CreateSubscription` — same hardcoded-owner convention as
  `Publication.Owner`, slice 422); four new columns
  `subpasswordrequired`/`subrunasowner`/`suborigin`/`subfailover` render the
  upstream `CREATE SUBSCRIPTION` defaults (`t`/`f`/`any`/`f` respectively) —
  goopg tracks none of these per-subscription, so the *default* is the only
  value that can ever be correct today.
- Test: extended `TestPort_PgDumpConnectionSetup` with `CREATE SUBSCRIPTION
  goopg_sub1 CONNECTION 'host=localhost port=5432 dbname=goopg_remote'
  PUBLICATION goopg_pub1 WITH (connect = false, slot_name = goopg_sub1)`
  (`connect = false` keeps this pure catalog registration — goopg's
  `execCreateSubscription` never dials the conninfo target either way, the
  apply-launcher wake it triggers is async and out of band) plus two new
  assertions: the exact `CREATE SUBSCRIPTION ... WITH (connect = false,
  slot_name = 'goopg_sub1', streaming = off, synchronous_commit = local);`
  line (verified byte-for-byte against real pg_dump 18.3 — note
  `dumpSubscription`'s `slot_name` value is a **string literal**
  (`appendStringLiteralAH`), not an identifier, unlike most other `WITH`
  options) and `ALTER SUBSCRIPTION goopg_sub1 OWNER TO postgres;`.
- Gates: `go build ./...`/`go vet ./...` clean; `internal/config`+
  `internal/catalog`+`internal/executor`+`internal/server`+`internal/initdb`
  suites PASS; `TestPort_PgDumpConnectionSetup` PASS; manual live repro
  against `./bin/goopg` + real `psql`/`pg_dump` 18.3 confirmed both the
  `is_superuser` startup value and the `pg_subscription`/`pg_database` OID
  match before writing the automated fixture; TPC-H spotcheck Q12=2/Q13=33
  PASS; pgbench smoke = pre-commit hook.
- Deferred (ledger row appended): `catalog.Subscription.Owner` is always the
  bootstrap superuser (10), same limitation as `Publication.Owner`.
  `dispatch_extended.go`'s extended-protocol path has no `SET SESSION
  AUTHORIZATION`/`SET ROLE` role-tracking at all (pre-existing, not
  introduced by this loop) — a client using the extended protocol to switch
  roles mid-session wouldn't get `NonSuperuserRole`/`is_superuser` updated
  either. `catalog.InMemory.DBOID()` vs. `pg_database.oid`'s
  `FirstUserOID`-hardcoded value remaining two independently-tracked
  "database identity" numbers (rather than one) is itself a latent
  multi-database-support gap — harmless today because goopg is
  single-live-database in practice, but would misbehave the moment either
  concept needs to vary per connected database.

## Loop #63 — extended-protocol `SET ROLE`/`SET SESSION AUTHORIZATION` role-tracking

Closes the loop #60 row's "`dispatch_extended.go`'s extended-protocol path
has no `SET SESSION AUTHORIZATION`/`SET ROLE` role-tracking at all" deferral.

### Investigation

The simple-query protocol tracks `connTx.NonSuperuserRole` (and the
reportable `is_superuser` GUC) for `SET ROLE`/`SET SESSION AUTHORIZATION`/
`RESET ROLE`/`RESET SESSION AUTHORIZATION` via string-matching cases in
`server/query.go`'s `handleQuery` (single statements) that run before the
parser/executor. Live-probing the extended-query protocol (Parse/Bind/
Execute/Sync) with the exact same statements surfaced that this path is
**not** a graceful no-op but an outright failure:

- `extended.go`'s `executeExtendedQuery` has its own string-matching fast
  path (`SHOW`/`SET`/`RESET`, mirroring `query.go`'s) with **no** cases for
  `SET ROLE`/`SET SESSION AUTHORIZATION`/`RESET ROLE`/`RESET SESSION
  AUTHORIZATION`. `SET ROLE some_role` therefore fell into the generic
  `case strings.HasPrefix(upper, "SET ")` branch, which calls `splitSet` and
  treats `ROLE`/`SESSION` as a GUC name — `sess.Set("role", "some_role",
  false)` fails since `"role"` isn't a registered GUC, so the statement
  errors with `22023 unrecognized configuration parameter "ROLE"` instead of
  updating privilege state. Confirmed by reverting the fix and re-running
  the new regression test (RED): `SERROR ... unrecognized configuration
  parameter "ROLE"`.
- Even if a statement instead reached the executor path
  (`executeExtendedQueryViaExecutor` → `utilitySettingsOp`,
  `internal/executor/operators_utility_settings.go`), `SET ROLE`/`RESET
  ROLE` were unconditional no-ops (`"role" — no-op: goopg has no role
  management.`, M0097-0071) — and the parser (`internal/parser/parser.go`)
  discarded the role name entirely (`_, _ = p.parseIdent()`, forcing
  `s.Default = true` unconditionally), so even wiring the executor case
  would have had no role name to act on. This is the SAME executor entry
  point multi-statement simple-query batches use
  (`dispatchSimpleQueryViaExecutor`), so `SET ROLE foo; TRUNCATE bar;` in one
  simple-query message was equally broken — not extended-protocol-specific
  as the loop #60 row assumed, just never observed because no fixture
  issues `SET ROLE` as anything but a lone single statement.

### Fix

- **Parser** (`internal/parser/parser.go`, `parseSet`): `SET ROLE rolename`
  now captures the role name into `s.Value` (or sets `s.Default = true` for
  the literal `DEFAULT` keyword), instead of discarding it and hardcoding
  `Default = true`. `NONE`/`POSTGRES`/empty are treated as resets by the
  *consumer*, not the parser (mirrors how `SET SESSION AUTHORIZATION`
  already splits parsing from reset-value interpretation).
- **Executor context** (`internal/executor/context.go`): new
  `SetRole func(role string)` field, sibling of the pre-existing
  `SetSessionAuthorization` — same contract (role name, or `""` to restore
  superuser).
- **Executor** (`internal/executor/operators_utility_settings.go`):
  `utilitySettingsOp`'s `SetStmt`/`ResetStmt` `"role"` cases now call
  `ctx.SetRole` (reset-value detection: `""`/`NONE`/`POSTGRES`) instead of
  being unconditional no-ops. This fixes the multi-statement simple-query
  batch gap too, not just extended-protocol.
- **Simple-query dispatch** (`internal/server/dispatch.go`):
  `ectx.SetRole = ectx.SetSessionAuthorization` — `SET ROLE` and `SET
  SESSION AUTHORIZATION` flip `connTx.NonSuperuserRole` identically, so they
  share one closure.
- **Extended-protocol fast path** (`internal/server/extended.go`): added
  `SET SESSION AUTHORIZATION`/`SET LOCAL SESSION AUTHORIZATION`/`SET ROLE`
  cases to `executeExtendedQuery`'s switch (checked before the generic `SET
  LOCAL `/`SET ` cases, mirroring `query.go`'s ordering) plus `RESET SESSION
  AUTHORIZATION`/`RESET ROLE` (before the generic `RESET `/`RESET ALL`
  cases). New shared helper `setSessionAuthorizationFastPath` factors the
  role-parsing/reset-detection logic used by both the plain and `LOCAL`
  forms. `executeExtendedQuery`/`executeExtendedQueryViaExecutor`/
  `handleExecuteFrame` all gained a `connTx *connTxState` parameter, threaded
  from `server.go`'s `serveConn` (which already constructs `connTx` per
  connection — it just wasn't passed to the extended-query call chain).
- **Extended-protocol executor path** (`internal/server/dispatch_extended.go`):
  `executeExtendedQueryViaExecutor`'s `ectx` now wires
  `NonSuperuserRole`/`SetSessionAuthorization`/`SetRole` from `connTx`
  exactly like `dispatch.go`'s simple-query executor path — so a `SET ROLE`
  that reaches the executor (rather than the fast-path switch) also updates
  privilege state, and — as a side effect — object-ownership privilege
  checks that read `ctx.NonSuperuserRole` (e.g. TRUNCATE ownership,
  M0118-0008) now work correctly for statements issued via the extended
  protocol, not just simple-query.
- Test: `internal/server/extended_set_role_test.go`'s
  `TestExtendedProtocolSetRoleTracksNonSuperuserRole` drives `SET ROLE` /
  `RESET ROLE` / `SET SESSION AUTHORIZATION` / `RESET SESSION AUTHORIZATION`
  through raw Parse/Bind/Execute/Sync frames and asserts `SHOW is_superuser`
  (over the simple-query protocol) flips off/on identically to the existing
  simple-query behaviour. Verified RED (via a temporary revert) before the
  fix: `unrecognized configuration parameter "ROLE"`. `internal/parser/
  parser_test.go`'s `TestParseShowSetReset` gained `set role name`/`set role
  default`/`set role none`/`reset role` subtests pinning the parser capture
  fix.
- Gates: `go build ./...`/`go vet ./...` clean; `internal/parser`+
  `internal/executor`+`internal/server` suites PASS;
  `TestPort_IsolationTruncateConflict` PASS (confirms the parser/executor
  `SET ROLE` change doesn't regress the existing simple-query-protocol
  ownership-check spec); `TestPort_PgDumpConnectionSetup` PASS; TPC-H
  spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook.
- Deferred (ledger row appended): `SET LOCAL ROLE`/`SET LOCAL SESSION
  AUTHORIZATION` are parsed with `s.Local` set but neither the executor nor
  either wire-protocol path gives `LOCAL`'s transaction-scoped-revert
  semantics any special handling — the role change persists past the
  transaction boundary exactly like a non-`LOCAL` `SET ROLE` would (a
  pre-existing limitation for `SESSION AUTHORIZATION`, now also true for
  `ROLE` by construction, not a new regression). `Subscription.Owner`/
  `Publication.Owner` non-bootstrap-role tracking and the
  `DBOID()`-vs-`FirstUserOID` split (both noted in loop #60) remain open —
  untouched by this loop.

## Loop #64: `SET LOCAL ROLE` / `SET LOCAL SESSION AUTHORIZATION` transaction-scoped revert

Closes the loop #63 row's deferral: `SET LOCAL ROLE`/`SET LOCAL SESSION
AUTHORIZATION` must revert to the pre-transaction role at COMMIT *and*
ROLLBACK, mirroring PostgreSQL's `GUC_ACTION_LOCAL` stack (`guc.c`) — the
same "discard the transaction-scoped layer at xact end" fidelity
`config.SessionRegistry`'s `local` map + `EndTransaction()` already provides
for ordinary GUCs (see `internal/config/session.go`).

**A more severe bug surfaced during live repro, fixed in the same loop
(same root cause, same call sites — not scope creep):** `SET LOCAL ROLE
<name>` sent as its own simple-query message — the common client shape;
psql and most drivers send one statement per wire message — **raised
`unrecognized configuration parameter "role"` instead of doing anything.**
Confirmed live against a real server: `psql -c "SET LOCAL ROLE
probe_role"` errored; only the rare case of cramming `BEGIN; SET LOCAL
ROLE …; COMMIT;` into one `-c` string (a single Simple Query wire message
with an internal `;`, which reroutes to the parser-based executor) avoided
the bug. Root cause: `server/query.go`'s `handleQuery` fast-path switch had
a dedicated `"SET LOCAL SESSION AUTHORIZATION "` case but no analogous
`"SET LOCAL ROLE "` case, so it fell through to the generic `"SET LOCAL "`
case, which calls `handleSet` → `splitSet("ROLE probe_role")` →
`sess.Set("role", "probe_role", true)`. `"role"` is not a
`config.Registry` variable (`session_authorization` is registered;
`role`/`SET ROLE` is tracked entirely out-of-band via
`connTx.NonSuperuserRole`), so `Set` returned `unrecognized configuration
parameter "role"`. The identical gap existed in
`internal/server/extended.go`'s extended-protocol fast-path switch (added
in loop #63, same missing case).

### Fix

- **`internal/server/conn_tx.go`**: new `connTxState.LocalRolePriorValue
  *string` field — non-nil means "the pre-transaction `NonSuperuserRole`
  value to restore at end-of-transaction, captured by the FIRST LOCAL role
  change this transaction." New method `SnapshotLocalRoleIfNeeded(local
  bool)`: no-op unless `local` is true, an explicit transaction is active,
  and no snapshot has been taken yet this transaction (mirrors PostgreSQL's
  stack semantics: a second `SET LOCAL` in the same transaction does not
  move the restore target — it still reverts to the value from *before* the
  first LOCAL change). `End()` (the shared COMMIT/ROLLBACK teardown, called
  unconditionally at every commit/rollback site including connection
  teardown) restores `NonSuperuserRole` from the snapshot and clears the
  pointer, alongside the existing `PendingEnumValues` etc. resets — so a
  stale snapshot can never leak into the connection's next transaction even
  on paths that skip the `EndLocalTransaction` hook (e.g. teardown
  rollback).
- **`internal/executor/context.go`**: `SetSessionAuthorization`/`SetRole`
  gained a `local bool` parameter so the LOCAL flag reaches the server-side
  closures that own `connTx`.
- **`internal/executor/operators_utility_settings.go`**: `SetStmt`'s
  `"role"`/`"session_authorization"` branches now pass `stmt.Local` through;
  `ResetStmt`'s branches pass `false` (RESET has no LOCAL form in PG's
  grammar).
- **`internal/server/dispatch.go` / `dispatch_extended.go`**: the
  `SetSessionAuthorization`/`SetRole` closures call
  `connTx.SnapshotLocalRoleIfNeeded(local)` before mutating
  `connTx.NonSuperuserRole`. `EndLocalTransaction` (previously a bare alias
  `= sess.EndTransaction`) is now a closure that calls `sess.EndTransaction()`
  (unchanged, ordinary-GUC local-layer discard) and then re-syncs
  `ectx.NonSuperuserRole` + the reportable `is_superuser` GUC from
  `connTx.NonSuperuserRole` — picking up whatever `connTx.End()` (called by
  the caller immediately before `EndLocalTransaction()` at every call site)
  just restored. Runs unconditionally on every COMMIT/ROLLBACK (cheap,
  idempotent when no LOCAL role change occurred this transaction).
- **`internal/server/query.go`**: new `"SET LOCAL ROLE "` case (mirroring
  the existing `"SET LOCAL SESSION AUTHORIZATION "` case) fixes the
  unrecognized-parameter bug for the single-statement simple-query path;
  both it and the existing `"SET LOCAL SESSION AUTHORIZATION "` case now
  call `connTx.SnapshotLocalRoleIfNeeded(true)` before mutating
  `NonSuperuserRole`.
- **`internal/server/extended.go`**: new `"SET LOCAL ROLE "` case fixes the
  identical bug in the extended-protocol fast path. `setSessionAuthorizationFastPath`
  gained a `local bool` parameter (calls `SnapshotLocalRoleIfNeeded`); new
  sibling helper `setRoleFastPath` (same shape, but `"NONE"` — not
  `"RESET"` — is `SET ROLE`'s reset keyword) replaces the previously
  inline, duplicated `"SET ROLE "` case and backs both `"SET ROLE "` and
  the new `"SET LOCAL ROLE "` case.
- Verified live end-to-end against a real running server (`psql`, one
  statement per message — the shape that exposed the bug): `SET LOCAL
  ROLE` alone no longer errors; inside `BEGIN`/`COMMIT` and
  `BEGIN`/`ROLLBACK` it correctly reverts `is_superuser` to its
  pre-transaction value; two chained `SET LOCAL ROLE` calls in one
  transaction still revert to the pre-transaction value (not the first
  LOCAL value); a plain (non-LOCAL) `SET ROLE` continues to persist past
  COMMIT exactly as before (regression-checked, not a behavior change).
- Tests: `internal/server/conn_tx_local_role_test.go`
  (`TestConnTxStateSnapshotLocalRoleIfNeeded`,
  `TestConnTxStateEndRestoresLocalRole` — unit-level, no wire protocol) +
  `internal/server/set_local_role_test.go`
  (`TestSimpleProtocolSetLocalRoleAloneDoesNotError`,
  `TestSimpleProtocolSetLocalRoleRevertsAtCommitAndRollback` — full
  wire-protocol proof against a storage-backed test server, one statement
  per `Query` message, matching the exact shape that exposed the bug).
- Gates: `go build ./...`/`go vet ./...` clean; `internal/server`+
  `internal/executor` suites PASS; `go test -race -count=1
  ./internal/wal/... ./internal/mvcc/...` PASS (connTxState touches
  transaction lifecycle); `TestPort_IsolationTruncateConflict` PASS (no
  regression to the SET ROLE ownership-check path); `TestPort_
  PgDumpConnectionSetup` PASS; TPC-H spotcheck Q12=2/Q13=33; pgbench smoke
  = pre-commit hook.
- Deferred (ledger row appended): extended-protocol `BEGIN`/`COMMIT`/
  `ROLLBACK` are intercepted before reaching the executor and returned as
  no-op command tags (`dispatch_extended.go`'s `planner.Transaction`
  branch) — `connTx.active` never becomes true from a purely
  extended-protocol transaction, so `SET LOCAL ROLE` issued entirely over
  the extended protocol still has no transaction boundary to revert at
  (behaves like a plain `SET`, matching the pre-existing, already-documented
  "extended protocol is auto-commit-per-statement" architectural
  limitation — not a new gap, and outside this loop's scope). Also
  unchanged from loop #60: `Subscription.Owner`/`Publication.Owner`
  non-bootstrap-role tracking and the `DBOID()`-vs-`FirstUserOID` split.

## Loop #65: `Publication.Owner`/`Subscription.Owner` non-bootstrap-role tracking (DU-002 slice 424)

Closes the loop #60/#63/#64 rows' recurring deferral: `CREATE
PUBLICATION`/`CREATE SUBSCRIPTION` always stamped the bootstrap superuser
OID (10) as owner, even when the issuing session had switched roles via
`SET ROLE`/`SET SESSION AUTHORIZATION`. Loop #63/#64 gave a connection's
effective role somewhere to live (`executor.Context.NonSuperuserRole`,
kept in sync by both wire protocols); this loop is the first to read it at
a DDL-execution site rather than just a GUC-reporting one, mirroring
PostgreSQL's `GetUserId()`-as-owner convention (`CreatePublication`,
`postgres/src/backend/commands/publicationcmds.c`).

### Fix

- **`internal/catalog/pubsub.go`**: new `PubSub.CreatePublicationAsOwner(name,
  tables, opts, owner uint32)` / `CreateSubscriptionAsOwner(name, conninfo,
  publications, slotName, enabled, owner uint32)` — the existing
  `CreatePublication`/`CreateSubscription` become one-line `owner: 10`
  wrappers, so every caller that doesn't know about session role state
  (e.g. any future internal/test caller) keeps the pre-existing
  bootstrap-superuser-owned behavior unchanged.
- **`internal/executor/operators_ddl.go`**: new `ddlOp.currentDDLOwnerOID()`
  helper — returns `o.ctx.Catalog.RoleOID(o.ctx.NonSuperuserRole)` when a
  non-superuser role is in effect, else 10. An unresolvable role name (should
  be unreachable — `NonSuperuserRole` is only ever set from a role that
  already passed validation at `SET ROLE` time) falls back to the bootstrap
  superuser rather than fabricating a bogus owner OID.
  `execCreatePublication`/`execCreateSubscription` now call the `...AsOwner`
  variants with `o.currentDDLOwnerOID()`.
- No wire-protocol or catalog-view changes needed — `pg_publication.pubowner`/
  `pg_subscription.subowner` (`internal/initdb/replication_views.go`, from
  loop #59/#60) already render `pub.Owner`/`sub.Owner`; they just receive a
  non-bootstrap value now when applicable.

### Tests

`internal/executor/operators_ddl_pubsub_test.go`:
- `TestCreatePublicationOwnerDefaultsToBootstrapSuperuser` — pins the
  pre-existing behavior (no `SET ROLE` in effect → owner stays 10), guarding
  against this loop's change regressing the common superuser-session case.
- `TestCreatePublicationOwnerTracksEffectiveRole` — with
  `ctx.NonSuperuserRole` set to a registered non-superuser role, `CREATE
  PUBLICATION` stamps that role's OID as owner.
- `TestCreateSubscriptionOwnerTracksEffectiveRole` — same assertion for
  `CREATE SUBSCRIPTION`.

### Gates

`go build ./...` clean; `internal/catalog`+`internal/executor`+
`internal/server` suites PASS; TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench
smoke = pre-commit hook.

### Deferred (ledger row appended)

`ALTER PUBLICATION ... OWNER TO`/`ALTER SUBSCRIPTION ... OWNER TO` are still
unimplemented — a publication/subscription's owner is fixed at CREATE time
only, with no way to reassign it afterward (PostgreSQL's
`AlterPublicationOwner`/`AlterSubscriptionOwner`,
`publicationcmds.c`/`subscriptioncmds.c`, have no goopg counterpart). The
`DBOID()`-vs-`pg_database.oid`'s `FirstUserOID` split (loop #60) remains
open, untouched by this loop.

## Loop #67: `ALTER PUBLICATION`/`ALTER SUBSCRIPTION ... OWNER TO` (DU-002 slice 425)

Closes the loop #65 row's own deferral: a publication/subscription's owner
was fixed at `CREATE` time only, with no way to reassign it afterward.
Mirrors PostgreSQL's `AlterPublicationOwner`/`AlterSubscriptionOwner`
(`publicationcmds.c`/`subscriptioncmds.c`), scoped to the `OWNER TO` action —
the only one goopg models for these two object types (same scoping
`AlterCollationStmt`/`AlterAggregateOwnerStmt` use: RENAME TO, SET, and the
various `ADD`/`DROP` table-list forms stay no-ops).

### A pre-existing dead parse path, found while wiring this

Before this loop, `parseAlter`'s generic ALTER compatibility-stub loop
listed `"publication"`/`"subscription"` in its ident set alongside
`"schema"`/`"collation"`/`"domain"`/etc, matched via
`p.acceptIdentKeyword(objIdent)`. `acceptIdentKeyword` only matches
`TokenKind == TokenIdent`. But `"publication"`/`"subscription"` are
registered *keywords* (`KwPublication`/`KwSubscription` — needed by `CREATE
SUBSCRIPTION ... PUBLICATION p [, p2 ...]`'s own grammar), so they lex as
`TokenKeyword`, never `TokenIdent`. That branch could therefore never match:
`ALTER PUBLICATION`/`ALTER SUBSCRIPTION`, in *any* form (not just `OWNER
TO`), fell through every other check in `parseAlter` down to the ALTER-TABLE
default and errored with `expected keyword table` — not the silent no-op
the surrounding comment claimed. This was invisible until now because no
test exercised `ALTER PUBLICATION`/`ALTER SUBSCRIPTION` at all.

### Fix

- **`internal/parser/ast.go`**: new `AlterPublicationOwnerStmt` /
  `AlterSubscriptionOwnerStmt` — `{Name string; NewOwner string}`, `NewOwner`
  using the same `"current_user"` sentinel `AlterCollationStmt`/
  `AlterAggregateOwnerStmt` use for `CURRENT_USER`/`SESSION_USER`/
  `CURRENT_ROLE`. `Name` is a plain string (not the schema-qualified
  `ObjectName` collation/aggregate use) — publications/subscriptions are
  unqualified, matching `CreatePublicationStmt.Name`/
  `CreateSubscriptionStmt.Name`.
- **`internal/parser/ddl.go`** (`parseAlter`): new dedicated case, inserted
  before the generic compatibility-stub loop (which had its now-dead
  `"publication"`/`"subscription"` entries removed). Uses
  `p.acceptKeyword(KwPublication)`/`p.acceptKeyword(KwSubscription)` instead
  of the broken `acceptIdentKeyword`. Parses `name`, then either `OWNER TO
  ...` (builds the new stmt) or drains to the statement end as a no-op
  (RENAME TO / SET / ADD|DROP|SET TABLE / REFRESH PUBLICATION / ...),
  matching the generic stub's prior behavior for every non-OWNER form.
- **`internal/catalog/pubsub.go`**: new `PubSub.SetPublicationOwner(name
  string, owner uint32) error` / `SetSubscriptionOwner(...)` — returns
  `ErrPublicationNotFound`/`ErrSubscriptionNotFound` for an unknown name,
  otherwise updates `Owner` in place under the existing `mu` lock.
- **`internal/executor/operators_ddl.go`**: new `ddlOp.resolveNewOwnerOID`
  helper — the `"current_user"` → 10 / else `Catalog.RoleOID` lookup
  (42704 on an unresolvable name) that `execAlterCollation`/
  `execAlterAggregateOwner` each duplicate inline; `execAlterPublicationOwner`
  / `execAlterSubscriptionOwner` call it and then
  `PubSub.SetPublicationOwner`/`SetSubscriptionOwner`, surfacing a
  not-found error as 42704 (undefined_object).
- **`internal/planner/planner.go`**: the existing pub/sub `case` arm (which
  already routes `CreatePublicationStmt`/`DropPublicationStmt`/
  `CreateSubscriptionStmt`/`DropSubscriptionStmt` to a `DDL` plan node)
  extended with the two new statement types. Parsing and planning are
  separate dispatches — a statement that parses fine but isn't in this
  `case` list surfaces as `0A000 unsupported statement type` at `Plan()`
  time, which is exactly what happened on the first test run before this
  edit.
- No wire-protocol or catalog-view change: `pg_publication.pubowner`/
  `pg_subscription.subowner` already render `pub.Owner`/`sub.Owner` live.
  Like `CREATE`/`DROP PUBLICATION`/`SUBSCRIPTION`, the mutation is
  in-memory-only — `PubSub` has no WAL/restart persistence at all yet (see
  Deferred below).

### Tests

- **`internal/parser/alter_pubsub_owner_test.go`**: `TestParseAlterPublicationOwner`
  (name + `CURRENT_USER`/`SESSION_USER`/`CURRENT_ROLE`/literal-role
  variants), `TestParseAlterSubscriptionOwner`, and
  `TestParseAlterPublicationOtherFormsStillNoop` — pins that RENAME TO / ADD
  TABLE / SET / REFRESH PUBLICATION still parse as the pre-existing
  `AlterTableStmt{}` no-op, guarding against the new OWNER TO case
  accidentally swallowing other forms.
- **`internal/executor/operators_ddl_pubsub_test.go`**: `TestAlterPublicationOwnerTo`
  / `TestAlterSubscriptionOwnerTo` (owner actually changes),
  `TestAlterPublicationOwnerToUnknownRoleErrors` (42704 on a role that
  doesn't exist), `TestAlterPublicationOwnerToUnknownPublicationErrors`
  (42704 on a publication that was never CREATEd).

### Gates

`go build ./...` clean; `internal/parser`+`internal/catalog`+
`internal/planner`+`internal/executor`+`internal/server` suites PASS; TPC-H
spotcheck Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook.

### Deferred (ledger row appended)

`PubSub` (`catalog.PubSub`) still has zero WAL/restart persistence — every
publication/subscription (and now its `OWNER TO` change) vanishes on
restart, same pre-existing gap `CREATE`/`DROP PUBLICATION`/`SUBSCRIPTION`
have always had; this loop doesn't reopen it, just inherits it. The
`DBOID()`-vs-`FirstUserOID` split (loop #60) remains open, untouched.

## Loop #68: `PubSub` WAL/restart persistence (CREATE/DROP/ALTER OWNER TO)

Closes the loop #67 row's own deferral: `catalog.PubSub` had zero WAL/restart
persistence, so every `CREATE PUBLICATION`/`CREATE SUBSCRIPTION` (and any
subsequent `DROP`/`ALTER ... OWNER TO`) vanished on the next server restart.
Mirrors the CREATE/DROP/ALTER COLLATION restart-persistence pattern (loop
#48/#50): a new WAL record family per DDL verb + a post-physical-replay
recovery driver that re-applies each record to the in-memory registry.

### Why PubSub gets its own recovery driver, not an interface

Every other DU-002 restart-persistence driver (`collation_ddl_recovery.go`,
`aggregate_ddl_recovery.go`, ...) takes `cat catalog.Catalog` and type-asserts
to a small interface (`collationRegistryRecovery`, ...), because `catalog.Catalog`
is an interface with more than one implementation in tests. `catalog.PubSub`
has exactly one implementation — it's a concrete struct
(`internal/executor/context.go`'s `Context.PubSub *catalog.PubSub`), not part
of the `Catalog` interface at all — so `replayPubSubDDLRecords` in the new
`internal/initdb/pubsub_ddl_recovery.go` takes `*catalog.PubSub` directly and
calls its new `...DuringRecovery` methods without an interface indirection.

### New WAL record kinds (50-55)

`internal/wal/recovery.go`: `RecordKindCreatePublication` (50),
`RecordKindDropPublication` (51), `RecordKindAlterPublicationOwner` (52),
`RecordKindCreateSubscription` (53), `RecordKindDropSubscription` (54),
`RecordKindAlterSubscriptionOwner` (55) — the next free byte values after
`RecordKindAlterAggregateOwner` (49). Like collation/aggregate, these have no
per-object on-disk file namespace, so `applyRecord`'s physical redo path is a
no-op (`return false, nil`) for all six; only the recovery driver interprets
them. `EncodeCreatePublication`/`DecodeCreatePublication` carry the OID,
owner OID, the four `Publication` boolean flags packed into one byte, the
name, and the qualified-table-name list (2-byte count + length-prefixed
strings, same shape `EncodeCreateAggregate` uses for `argTypes`).
`EncodeCreateSubscription`/`DecodeCreateSubscription` mirror this for
`Subscription` (conninfo, slot name, enabled flag, publications list).
`EncodeDropPublication`/`EncodeAlterPublicationOwner` and their subscription
counterparts are single-name (+ ownerOID for the OWNER TO case) records,
copied verbatim from `EncodeDropCollation`/`EncodeAlterCollationOwner`'s
shape.

### Recovery mutators (`internal/catalog/pubsub.go`)

`CreatePublicationDuringRecovery`/`CreateSubscriptionDuringRecovery` take the
full `*Publication`/`*Subscription` struct decoded from the WAL record
(preserving the pre-crash OID) and unconditionally overwrite the map entry
keyed by name, bumping `nextOID` past the recovered OID so a subsequent
`CREATE` after restart doesn't collide. Unlike collation's
OID-keyed-slice overwrite-by-OID logic, `PubSub.publications`/`subscriptions`
are already keyed by name, so overwriting by name is sufficient — a replay
that sees the same CREATE record twice (partial-then-full replay) just
re-writes the identical entry. `Drop...DuringRecovery`/
`Set...OwnerDuringRecovery` are thin discard-result wrappers around the
existing `DropPublication`/`SetPublicationOwner` (and subscription
counterparts), mirroring `DropCollationDuringRecovery`'s
"replay doesn't care whether the object was still present" tolerance.

### Wiring

- **`internal/executor/operators_ddl.go`**: `execCreatePublication`/
  `execCreateSubscription` now capture the `*Publication`/`*Subscription`
  the `...AsOwner` call returns (previously discarded) and, when
  `o.ctx.WAL != nil`, append the matching `Encode...` record.
  `execDropPublication`/`execDropSubscription` append their drop record
  after a successful `PubSub.Drop...` call (skipped on the `IF EXISTS`
  not-found path, matching every other DROP's WAL-skip-on-noop convention).
  `execAlterPublicationOwner`/`execAlterSubscriptionOwner` append their owner
  record after a successful `PubSub.Set...Owner` call — this closes the
  loop #65/#67 comments' explicit "ownership change is in-memory-only"
  caveat, now stale and updated in place.
- **`internal/initdb/open.go`**: `replayPubSubDDLRecords(filepath.Join(abs,
  "pg_wal"), pubsub)` runs immediately after `pubsub := catalog.NewPubSub()`
  and before `registerPublicationViews`/`registerSubscriptionViews` wire the
  virtual-view accessors against it. `PubSub` is not schema-scoped (keyed by
  name only, like a cast/transform, not like collation/conversion's
  namespace-OID key), so — unlike the collation/conversion/aggregate
  replay calls above it in `Open` — this one has no ordering dependency on
  `replaySchemaDDLRecords`.

### Tests

- **`internal/wal/pubsub_ddl_test.go`**: `TestEncodeDecode{Create,Drop}{Publication,Subscription}RoundTrip`
  + the two `AlterOwner` round-trip tests + `TestDecodePubSubRejectsWrongKindAndTruncatedPayload`
  (wrong-kind-byte and truncated-payload guards across all six record kinds),
  mirroring `collation_ddl_test.go`'s structure.
- **`internal/initdb/pubsub_ddl_recovery_test.go`**: full `Init`→`Open`→
  `WAL.Append`→`Close`→re-`Open` round trips (real data dir, real WAL
  flush+replay, no mocking) for CREATE PUBLICATION, CREATE+DROP PUBLICATION
  (drop cancels create), CREATE+ALTER-OWNER PUBLICATION (OID survives the
  owner change), and the three subscription analogues, plus
  `TestReplayPubSubDDLRecordsHandlesMissingWalDir`/`...NilPubSub` for the
  fresh-initdb / embedded-test-setup no-op paths. Mirrors
  `collation_ddl_recovery_test.go`'s structure exactly.

### Gates

`go build ./...` clean; `go test -race -count=1 ./internal/wal/...
./internal/mvcc/...` PASS (WAL-format-adjacent change); `internal/wal`+
`internal/catalog`+`internal/initdb`+`internal/executor`+`internal/parser`+
`internal/planner`+`internal/server` suites PASS; TPC-H spotcheck
Q12=2/Q13=33 PASS; pgbench smoke = pre-commit hook.

### Still open (unchanged)

The `DBOID()`-vs-`FirstUserOID` split (loop #60 row) remains open, untouched
by this loop. `SubscriptionRel` (tablesync state) rows still have no WAL
persistence either — out of scope for this slice (no pg_dump TAP fixture
exercises tablesync state across a restart; it's runtime apply-worker state,
not a `pg_dump`-visible catalog row).

## Loop #69: `CREATE`/`DROP EVENT TRIGGER` round-trip

Closes a fresh gap found by re-scanning the connsetup fixture's object-type
coverage for anything the pg_dump surface exercises but goopg had never
implemented: `pg_event_trigger` (`internal/catalog/catalog.go`) was a
scaffolded-but-always-empty virtual view (`VirtualRows = func() [][]string {
return nil }`) with a comment explicitly noting "goopg defines no event
triggers (no CREATE EVENT TRIGGER)" — i.e. the statement had never been
parsed at all, not merely a dump-fidelity gap.

### Scope

DDL only (`CREATE`/`DROP EVENT TRIGGER`); `ALTER EVENT TRIGGER`
(ENABLE/DISABLE/RENAME/OWNER TO) is out of scope for this loop (see
"Deferred" below) — mirrors how CREATE PUBLICATION/SUBSCRIPTION shipped many
loops before ALTER ... OWNER TO (loop #67) and WAL persistence (loop #68)
followed. goopg never fires event triggers (no DDL hook invokes `evtfoid`);
this is dump-fidelity only, matching the FDW/publication/subscription family.

### Grammar (postgres/src/backend/parser/gram.y `CreateEventTrigStmt`)

```
CREATE EVENT TRIGGER name ON event
  [WHEN filtervar IN (value [, ...]) [AND filtervar IN (value [, ...])]]
  EXECUTE {FUNCTION|PROCEDURE} funcname()
```

"EVENT" is unreserved and was not registered as a lexer keyword at all
(goopg's existing `DROP ... event ...` compat-stub entry only ever matched
the bare ident "event", never "event trigger" as two words — see the bug
below); it is matched via `p.acceptIdentKeyword("event")` like
"collation"/"transform", then `KwTrigger` (already registered,
`KwCatUnreserved`) is consumed explicitly.

### A pre-existing dead/broken path, found while wiring this

`DROP EVENT TRIGGER` already had an entry in the ident-based `DropCompatStmt`
stub loop (`"event"` in the object-type list, `internal/parser/ddl.go`) —
but with no continuation handling for the second word, unlike
"materialized"/"access". Since `TRIGGER` is a `KwCatUnreserved` keyword
token, `parseIdent()` (used by the name-list parser) would have silently
accepted it AS the object name, then choked on the real name as an
unexpected trailing token. This was unreachable/untested dead code before
this loop (CREATE EVENT TRIGGER never existed, so nothing could construct a
droppable event trigger), not a live regression. Fixed alongside the new
CREATE support — mirrors the "access method" continuation exactly, except
`KwTrigger` is `expectKeyword`'d (keyword-typed) rather than
`acceptIdentKeyword`'d (ident-typed).

### Catalog

New `catalog.EventTrigger` (`Name`, `OID`, `Event`, `Owner`, `FuncOID`,
`Enabled` — always `"O"`, `Tags`) + `RegisterEventTrigger` (42710 on a
duplicate name) / `DropEventTrigger` / `ListEventTriggers` (ordered by OID,
matching pg_dump's `getEventTriggers ... ORDER BY e.oid`) on `InMemory`,
mirroring `ForeignDataWrapper`'s registry shape one-for-one.
`pg_event_trigger.VirtualRows` now renders real rows instead of the
hardcoded `nil`; `evtfoid` renders as a bare OID — the `::regproc` cast in
pg_dump's own `getEventTriggers` query resolves it to a name (see the
regproc fix below).

### Executor

`execCreateEventTrigger` (`internal/executor/operators_ddl.go`) validates the
event name against PostgreSQL's fixed 5-value set
(`ddl_command_start`/`ddl_command_end`/`sql_drop`/`login`/`table_rewrite`,
42601 otherwise), the WHEN filter variable (only `"tag"` is recognised,
42601 otherwise), and the login-event tag-filtering restriction (`0A000`),
then resolves the trigger function via new `resolveEventTriggerFunc`
(mirrors `CreateEventTrigger`'s `LookupFuncName(stmt->funcname, 0, NULL,
false)` — a niladic user routine first via `Routines().LookupByName`, which
already searches every schema when unqualified, then the hand-curated
builtin `pg_proc` set) before calling `RegisterEventTrigger`. `DROP EVENT
TRIGGER` reuses the generic `DropCompatStmt` path: a new `"event trigger"`
case in `execDropCompat` calls `DropEventTrigger` (same pattern as
`"server"`/`"foreign-data wrapper"`). Planner: `*parser.CreateEventTriggerStmt`
added to the `DDL` case list (`DROP EVENT TRIGGER` already routed via the
pre-existing `DropCompatStmt` case).

### Real bug found and fixed via live-PG diff: plain `regproc` never
schema-qualified a user-defined function

Verified end-to-end against a freshly-`initdb`'d, live PG 18.3 instance
(distinct from goopg, both queried via the real `pg_dump`/`psql` binaries
under `postgres/local_install`) with the identical fixture (`CREATE FUNCTION
public.et_func() RETURNS event_trigger ...; CREATE EVENT TRIGGER et1 ON
ddl_command_start WHEN TAG IN ('CREATE TABLE', 'ALTER TABLE') EXECUTE
FUNCTION et_func();`). Real PG's dump line was `EXECUTE FUNCTION
public.et_func();`; goopg's was `EXECUTE FUNCTION et_func();` — a real,
observable divergence, not the loop #36/#38 `regoperator`/`regprocedure`
gap (already fixed) but its previously-unaudited sibling: the plain
`regproc` OID→name `CastExpr` branch (`internal/executor/expr.go`, distinct
code path from the `regprocedure` branch a few lines below it) resolved a
user-defined routine via `ctx.Catalog.Routines().LookupByOID` and returned
`r.Name` bare, with no `regObjectSchemaVisible` check at all. Fixed by
applying the exact same check the `regprocedure`/`regoperator` branches
already use: `regObjectSchemaVisible(ctx, r.Schema)` gates whether
`r.Schema + "." + r.Name` or bare `r.Name` is returned. Re-verified against
the same live PG 18.3 instance after the fix — dump lines are now
byte-identical (`EXECUTE FUNCTION public.et_func();` on both sides); the
only remaining diff was the pre-existing, unrelated `\restrict` token +
public-schema COMMENT/ACL lines that appear on any goopg-vs-fresh-initdb
comparison (bootstrap catalog differences, not this feature). This fix's
blast radius is every other `<oid>::regproc` cast on a user-defined
(non-builtin) function OID in the codebase — a strict fidelity
improvement, not a behavior change for any builtin (still bare, unaffected)
or any case where the routine happens to be schema-visible already.

### Tests

Parser: `internal/parser/event_trigger_test.go`
(`TestParseCreateEventTriggerSimple`, `...WhenTag`,
`...UnrecognizedFilterVar`, `TestParseDropEventTrigger`). Executor:
`internal/executor/operators_ddl_event_trigger_test.go`
(`TestCreateEventTriggerRegistersRow`,
`...DuplicateNameErrors`, `...UnknownFunctionErrors`,
`...UnrecognizedEventErrors`, `TestDropEventTriggerRemovesRow`).

### Gates

`go build ./...`/`go vet ./...` clean; `internal/parser`+`internal/catalog`+
`internal/planner`+`internal/executor`+`internal/server` suites PASS
(`-count=1`); `TestPort_PgDumpConnectionSetup` PASS (no regression from the
`regproc` schema-qualification fix); live PG 18.3 diff above (byte-identical
`EXECUTE FUNCTION public.et_func();`); TPC-H spotcheck Q12=2/Q13=33 PASS;
pgbench smoke = pre-commit hook.

### Deferred (ledger row appended)

`ALTER EVENT TRIGGER` (ENABLE/DISABLE/RENAME TO/OWNER TO) is entirely
unimplemented — every event trigger stays `evtenabled='O'` and owned by the
DDL-time role for its lifetime; real PG's `AlterEventTrigStmt`/`RENAME
TO`/`OWNER TO` forms (`event_trigger.c`) have no goopg counterpart. Also
deferred: the full `validate_ddl_tags`/`validate_table_rewrite_tags`
command-tag-list validation (goopg accepts any WHEN TAG value verbatim, no
membership check against PostgreSQL's real DDL command-tag enumeration) and
the `CreateEventTrigger` superuser privilege check (goopg does not gate this
DDL on role privilege, consistent with how the rest of this compat-only DDL
family — CREATE FOREIGN DATA WRAPPER, CREATE SERVER, etc. — does not enforce
its own real-PG superuser requirement either). No WAL/restart persistence
either (same gap CREATE PUBLICATION/SUBSCRIPTION had before loop #68) — an
event trigger vanishes on restart today.

## Loop #70 — `ALTER EVENT TRIGGER` (ENABLE/DISABLE/RENAME TO/OWNER TO)

Closes the loop #69 row's own "`ALTER EVENT TRIGGER` is entirely
unimplemented" resume point, mirroring the `ALTER PUBLICATION`/`ALTER
SUBSCRIPTION ... OWNER TO` pattern from loop #67.

### What landed

New parser `AlterEventTriggerStmt` (`internal/parser/ast.go`) with an
`Action` enum (`"disable" | "enable" | "enable_replica" | "enable_always" |
"rename" | "owner"`) plus `NewName`/`NewOwner`. `parseAlter()`
(`internal/parser/ddl.go`) gets a new `"event"` + `KwTrigger` branch — parsed
explicitly (not via the generic ident-based compat-stub loop, since `event
trigger` is two tokens, same as the existing DROP EVENT TRIGGER case) — for:

```
ALTER EVENT TRIGGER name DISABLE
ALTER EVENT TRIGGER name ENABLE [REPLICA|ALWAYS]
ALTER EVENT TRIGGER name RENAME TO new_name
ALTER EVENT TRIGGER name OWNER TO {new_owner|CURRENT_ROLE|CURRENT_USER|SESSION_USER}
```

`catalog.InMemory` gets three new registry mutators mirroring
`RegisterEventTrigger`: `SetEventTriggerEnabled(name, code)` (code is one of
PG's four `evtenabled` values, `"O"/"D"/"A"/"R"`), `SetEventTriggerOwner(name,
ownerOID)`, `RenameEventTrigger(name, newName)` (re-keys the
`map[string]*EventTrigger`). New sentinel errors `ErrEventTriggerNotFound` /
`ErrEventTriggerAlreadyExists` let `execAlterEventTrigger`
(`internal/executor/operators_ddl.go`) distinguish 42704 undefined_object
(unknown trigger name) from 42710 duplicate_object (RENAME TO an already-taken
name), reusing `resolveNewOwnerOID` for the `CURRENT_USER` sentinel. The
planner's `*parser.CreateEventTriggerStmt` DDL case
(`internal/planner/planner.go`) picks up `*parser.AlterEventTriggerStmt`
alongside it — this was the first failure mode hit in testing (`0A000:
unsupported statement type`), since a new `Stmt` type needs an explicit planner
allow-list entry before the executor ever sees it.

Since the pg_event_trigger virtual view (`internal/catalog/catalog.go`)
already renders `evtenabled`/`evtname`/`evtowner` straight from the
`EventTrigger` struct (added in loop #69), no view-rendering change was
needed — real pg_dump 18.3's own `dumpEventTrigger` does the rest: it appends
`\nALTER EVENT TRIGGER %s {DISABLE|ENABLE|ENABLE ALWAYS|ENABLE REPLICA};\n` to
the *same* archive entry's create-statement buffer whenever `evtenabled !=
'O'` (pg_dump.c `dumpEventTrigger`), so no separate "ALTER EVENT TRIGGER
ENABLE TO restore state" TOC entry exists.

### Live PG 18.3 verification

Manual server (`/tmp/goopg_evttrig_manual`, real `pg_dump`/`psql` from
`postgres/local_install/bin`, not goopg's own client) round-tripped
`CREATE FUNCTION ... RETURNS event_trigger` → `CREATE EVENT TRIGGER et1 ...`
→ `ALTER EVENT TRIGGER et1 DISABLE` → `ALTER EVENT TRIGGER et1 RENAME TO
et1_renamed` → `ALTER EVENT TRIGGER et1_renamed OWNER TO CURRENT_USER`,
confirming via `SELECT ... FROM pg_event_trigger` that `evtname`/`evtenabled`/
`evtowner` all updated. `pg_dump --no-owner --schema-only` then emitted:

```sql
CREATE EVENT TRIGGER et1_renamed ON ddl_command_start
   EXECUTE FUNCTION public.et_func();

ALTER EVENT TRIGGER et1_renamed DISABLE;
```

— byte-identical to PG 18.3's `dumpEventTrigger` format. Cycling through
`ENABLE ALWAYS` / `ENABLE REPLICA` / plain `ENABLE` confirmed each renders
correctly and that returning to the `'O'` (origin/default) state correctly
*omits* the trailing `ALTER EVENT TRIGGER` line (the `evtenabled != 'O'`
gate).

### Tests

Parser: `internal/parser/event_trigger_test.go`
(`TestParseAlterEventTrigger`, table-driven over all 6 forms including both
`CURRENT_USER`/`session_user` owner sentinels). Executor:
`internal/executor/operators_ddl_event_trigger_test.go`
(`TestAlterEventTriggerEnableDisable`, `...RenameTo` — including the 42710
duplicate-name path — `...OwnerTo`, `...UnknownNameErrors`).

### Gates

`go build ./...` clean; `internal/parser`+`internal/catalog`+
`internal/planner`+`internal/executor`+`internal/server` suites PASS;
`TestPort_PgDumpConnectionSetup` PASS (no regression); live PG 18.3 diff
above (byte-identical); TPC-H spotcheck Q12=2/Q13=33 PASS; pgbench smoke =
pre-commit hook.

### Still deferred

WAL/restart persistence for event triggers as a whole (CREATE, DROP, and now
ALTER) — an event trigger, including any ALTER'd state, still vanishes on
restart; no forcing fixture today (same gap noted in loop #69). The
`validate_ddl_tags`/superuser-privilege gaps from loop #69 are unaffected by
this loop.

## Loop #71 — Event trigger WAL/restart persistence

Closes the loop #70 row's own "WAL/restart persistence for event triggers …
still vanishes on restart" resume point, mirroring the PubSub WAL/restart
persistence pattern from loop #68 (`internal/wal/pubsub_ddl_test.go`,
`internal/initdb/pubsub_ddl_recovery.go`).

### What landed

Five new WAL record kinds in `internal/wal/recovery.go`:
`RecordKindCreateEventTrigger` (56), `RecordKindDropEventTrigger` (57),
`RecordKindAlterEventTriggerEnabled` (58; carries the raw
`evtenabled` code byte so DISABLE/ENABLE/ENABLE REPLICA/ENABLE ALWAYS all
share one record shape), `RecordKindAlterEventTriggerRename` (59), and
`RecordKindAlterEventTriggerOwner` (60), each with an `Encode*`/`Decode*`
pair following the existing publication/subscription record format
convention (length-prefixed strings, `binary.LittleEndian`). goopg has no
per-event-trigger on-disk file namespace — `catalog.InMemory`'s
`eventTriggers` map is a pure in-memory registry, like `catalog.PubSub` — so
`wal.ApplyRecord`'s physical-redo switch treats all five kinds as a no-op,
same as the publication/subscription kinds.

A new post-replay recovery driver,
`internal/initdb/event_trigger_ddl_recovery.go`
(`replayEventTriggerDDLRecords`), walks the WAL once after physical replay
and re-applies each record via five new `*DuringRecovery` catalog mutators
(`internal/catalog/catalog.go`): `RegisterEventTriggerDuringRecovery` (takes
the OID from the record and advances `nextOID` via the existing
`advanceNextOIDLocked` helper, overwriting rather than erroring on a
name collision — mirrors `PubSub.CreatePublicationDuringRecovery`),
`DropEventTriggerDuringRecovery`, `SetEventTriggerEnabledDuringRecovery`,
`RenameEventTriggerDuringRecovery`, `SetEventTriggerOwnerDuringRecovery`
(all discard-error, idempotent-by-design counterparts to the loop #69/#70
mutators). `internal/initdb/open.go` calls the driver right after the
PubSub replay call — event triggers, like PubSub, are not schema-scoped, so
the ordering relative to schema replay doesn't matter, only that it runs
before the first client session can query `pg_event_trigger`.

The three executor sites now WAL-log their mutation before returning
success: `execCreateEventTrigger` and the `execDropCompat` `"event
trigger"` case (`internal/executor/operators_ddl.go`) each append one
record; `execAlterEventTrigger` appends one record per `switch s.Action`
arm (a small `logEnabled` closure covers the four ENABLE/DISABLE variants so
they share one call site). All four `o.ctx.WAL.Append` calls are `nil`-gated
(`if o.ctx.WAL != nil`) matching the existing PubSub/collation/aggregate DDL
sites — some embedded test harnesses run without a WAL writer at all.

Also added `catalog.InMemory.LookupEventTrigger(name)` (a deep-copy
single-row lookup, mirroring `PubSub.LookupPublication`) — the registry
previously only exposed `ListEventTriggers()`, which the recovery tests
needed a direct name-keyed accessor for.

### Live PG 18.3 verification

Manual server (`/tmp/evt-restart-data`, real `psql`/`pg_dump` from
`postgres/local_install/bin`): `CREATE FUNCTION et_func() RETURNS
event_trigger`, `CREATE EVENT TRIGGER mytrig ON ddl_command_start WHEN TAG
IN ('CREATE TABLE') EXECUTE FUNCTION et_func()`, `ALTER EVENT TRIGGER
mytrig DISABLE`, then `goopg stop` + `goopg start` against the same data
dir. `SELECT evtname, evtevent, evtenabled FROM pg_event_trigger` after
restart returned `mytrig|ddl_command_start|D` — all three CREATE-time
fields plus the DISABLE state survived. A second round exercised RENAME TO
+ OWNER TO CURRENT_USER + re-ENABLE, then another restart: `SELECT evtname,
evtenabled, evtowner::regrole FROM pg_event_trigger` returned
`renamedtrig|O|10`, confirming all three ALTER forms persist together.
(Incidental discovery, out of scope here: after restart `pg_dump`'s
`EXECUTE FUNCTION 131072()` no longer resolves to `et_func()`, because
`CREATE FUNCTION` itself has no WAL/restart persistence in goopg today — a
pre-existing gap that predates and is broader than this event-trigger slice;
not ledgered as a new row since it isn't specific to event triggers.)

### Tests

`internal/wal/event_trigger_ddl_test.go` pins the five record formats
(encode→decode round trip per kind, plus a wrong-kind/truncated-payload
guard test), mirroring `internal/wal/pubsub_ddl_test.go`.
`internal/initdb/event_trigger_ddl_recovery_test.go` exercises the full
`Init`→`Open`→append WAL→`Close`→`Open` cycle: CREATE alone, CREATE+DROP
(net registry effect must be "absent"), and CREATE+DISABLE+RENAME+OWNER
chained (OID must survive rename/owner changes unchanged), plus the
missing-WAL-dir and nil-catalog no-op guards, mirroring
`internal/initdb/pubsub_ddl_recovery_test.go`.

### Gates

`go build ./...` clean; `go vet ./...` clean; `internal/wal`+
`internal/catalog`+`internal/initdb`+`internal/executor`+`internal/planner`+
`internal/parser`+`internal/server` suites PASS; live PG 18.3 diff above
(byte-identical evtname/evtevent/evtenabled/evtowner survival across two
independent restart cycles); TPC-H spotcheck Q12=2/Q13=33 PASS (`scripts/
tpch-spotcheck.sh`); full pre-commit gate including pgbench TPC-B smoke PASS
(`scripts/ralph-precommit-test.sh`).

### Still deferred

The `validate_ddl_tags`/`validate_table_rewrite_tags` command-tag-list
validation and `CreateEventTrigger`'s superuser privilege check (both from
loop #69) remain untouched. Broader and out of scope: `CREATE FUNCTION`
itself has no WAL/restart persistence, so an event trigger's `evtfoid`
reference can dangle after a restart if the backing function was also
created post-initdb and not re-created — this is a pre-existing routines/
functions persistence gap, not new to this loop, and not specific to event
triggers.

## Loop #72 — DDL-command-tag validation + superuser enforcement (closes the loop #69 row)

Closes the loop #69 row's other two deferrals — `validate_ddl_tags`/
`validate_table_rewrite_tags` command-tag membership checking, and
`CreateEventTrigger`'s superuser privilege check — the only remaining open
thread on the `CREATE`/`ALTER`/`DROP EVENT TRIGGER` family before it moves to
strictly-additive follow-ups (e.g. the broader `CREATE FUNCTION` persistence
gap, tracked separately).

### What landed

**Command-tag table.** `internal/executor/cmdtag_table.go` is a
mechanically-generated (Python one-liner over
`postgres/src/include/tcop/cmdtaglist.h`, not hand-transcribed) Go map of
all 192 real PostgreSQL command tags to their `event_trigger_ok`/
`table_rewrite_ok` flags (`postgres/src/backend/tcop/cmdtag.c`'s
`tag_behavior[]`), keyed upper-case. `validateDDLTags`/
`validateTableRewriteTags` do a case-insensitive lookup (mirrors
`GetCommandTagEnum`'s `pg_strcasecmp` bsearch) and mirror PG's two distinct
error shapes exactly:

- `validate_ddl_tags` (ddl_command_start/ddl_command_end/sql_drop): an
  unrecognized tag is `42601` (`filter value "%s" not recognized for filter
  variable "tag"`); a recognized-but-disallowed tag (e.g. `VACUUM`, which PG
  never fires event triggers for) is `0A000` (`event triggers are not
  supported for %s`).
- `validate_table_rewrite_tags` (table_rewrite): PG's C implementation has
  **no** `CMDTAG_UNKNOWN` special case here (unlike `validate_ddl_tags`), so
  an unrecognized tag and a known-but-non-rewrite tag (e.g. `CREATE TABLE`,
  which is `event_trigger_ok` but not `table_rewrite_ok`) both fall through
  to the same `0A000` — goopg's `validateTableRewriteTags` reproduces this
  by not distinguishing "absent from the map" from "present with
  `tableRewriteOK: false`" (a bare map lookup returning the zero value
  covers both).

Wired into `execCreateEventTrigger` right after the existing event-name/
filter-variable/login-tag checks, keyed on `s.Event` the same way PG's
`CreateEventTrigger` dispatches to the two validators.

**Superuser enforcement.** Two checks, both using the existing
`Context.NonSuperuserRole` compat mechanism (the same one `ALTER OPERATOR
FAMILY ADD`/`AlterOperatorSetStmt`'s leakproof check already uses — goopg
tracks no per-role `rolsuper` attribute; only OID 10, the bootstrap
superuser, is ever superuser):

- `execCreateEventTrigger`: `NonSuperuserRole != ""` → `42501`
  (`permission denied to create event trigger "%s"`), checked first, before
  event-name validation — matches `CreateEventTrigger`'s own ordering
  (superuser check precedes everything else in the C function).
- `execAlterEventTrigger`'s `"owner"` case: the *resolved new owner OID*
  must be 10 (the only OID goopg's role model ever calls a superuser) → 42501
  (`permission denied to change owner of event trigger "%s"`). This mirrors
  `AlterEventTriggerOwner_internal`'s `superuser_arg(newOwnerId)` check,
  which is about the **target** role's superuser-ness, not the caller's — a
  session run *as* the bootstrap superuser can still be rejected if it tries
  to hand ownership to a non-superuser role.

`TestAlterEventTriggerOwnerTo` previously asserted `OWNER TO alice`
(non-superuser) succeeded — that assertion encoded a real PG semantic gap,
not a goopg feature to preserve, so it was rewritten to only exercise the
`CURRENT_USER` (→ OID 10) success case; the alice-target rejection moved to
its own new test.

### Live PG 18.3 verification

Fresh `initdb`+`pg_ctl` instance (not goopg;
`postgres/local_install/bin/{initdb,pg_ctl,psql}`,
`LD_LIBRARY_PATH=postgres/local_install/lib` to work around a libpq/psql
build-version mismatch in the prebuilt tree). Confirmed all four shapes
before writing any goopg code:

| SQL | Result |
|-----|--------|
| `WHEN TAG IN ('BOGUS TAG')` on `ddl_command_start` | `42601: filter value "BOGUS TAG" not recognized for filter variable "tag"` |
| `WHEN TAG IN ('VACUUM')` on `ddl_command_start` | `0A000: event triggers are not supported for VACUUM` |
| `WHEN TAG IN ('BOGUS TAG')` on `table_rewrite` | `0A000: event triggers are not supported for BOGUS TAG` (not 42601) |
| `WHEN TAG IN ('CREATE TABLE')` on `table_rewrite` | `0A000: event triggers are not supported for CREATE TABLE` |
| `WHEN TAG IN ('ALTER TABLE')` on `table_rewrite` | succeeds |
| `WHEN TAG IN ('create table')` (lower-case) | succeeds — case-insensitive |
| `CREATE EVENT TRIGGER ...` as a non-superuser login role | `42501: permission denied to create event trigger "..."` |
| `ALTER EVENT TRIGGER ... OWNER TO <non-superuser role>` | `42501: permission denied to change owner of event trigger "..."` |

### Tests

`internal/executor/operators_ddl_event_trigger_test.go`:
`TestCreateEventTriggerNonSuperuserErrors` (42501 on CREATE as a
`NonSuperuserRole` session), `TestCreateEventTriggerTagValidation`
(table-driven, all 8 rows above plus the `sql_drop`-shares-the-ddl-table
case), `TestAlterEventTriggerOwnerToNonSuperuserErrors` (42501 on `OWNER TO
alice`, registry left unchanged). `TestAlterEventTriggerOwnerTo` narrowed to
just the `CURRENT_USER` case (see above).

### Gates

`go build ./...` clean; `internal/parser`+`internal/catalog`+
`internal/planner`+`internal/executor`+`internal/wal`+`internal/initdb`+
`internal/server` suites PASS; TPC-H spotcheck Q12=2/Q13=33 PASS; full
pre-commit gate incl. pgbench TPC-B smoke PASS.

### Still deferred

Nothing left open on the `CREATE`/`ALTER`/`DROP EVENT TRIGGER` family
itself. The broader, pre-existing `CREATE FUNCTION` WAL/restart persistence
gap (discovered in loop #71, out of scope for the event-trigger thread)
remains open under its own resume point.

## Loop #73 — CREATE/ALTER/DROP FUNCTION + CREATE/ALTER PROCEDURE WAL/restart persistence

Closes the loop #71 row's own resume point: `catalog.Routines` (function/
procedure registry) had zero restart persistence, so every user-defined
routine vanished on server restart — including a routine an event trigger's
`evtfoid` referenced. Mirrors the event-trigger WAL/restart pattern (loop
#71) with one structural departure driven by the object's size.

### What landed

Four new WAL record kinds in `internal/wal/recovery.go`:
`RecordKindCreateFunction` (61), `RecordKindDropFunction` (62),
`RecordKindAlterFunctionRename` (63), `RecordKindAlterFunctionFlags` (64).
Unlike every prior DU-002 WAL-persisted DDL family (event triggers,
aggregates, collations, publications/subscriptions, casts/transforms/
conversions), `CREATE FUNCTION` has too many fields for a flat positional
`Encode(a, b, c, ...) []byte` signature — a full parallel arg list (name,
type incl. numeric typmod, mode, default), a return type, a body, and 8+
scalar attributes — so `CreateFunctionPayload`/`FunctionArgPayload` are
struct-based instead, following the `CreateIndexPayload` precedent
(M0079-0001) rather than the `EncodeCreateAggregate`-style positional one.
`Body` gets a 4-byte length prefix (every other string field keeps the
usual 2-byte one) since a plpgsql body can plausibly exceed 65535 bytes —
the one field in this family where the existing 2-byte convention could
silently truncate real data. `DropFunction` carries the resolved OID, not
name+signature: `DROP FUNCTION`'s own overload resolution already ran live
before the WAL record is written, so replaying by OID sidesteps
re-resolving a possibly-ambiguous bare name against a registry that may
only be partially replayed at that point in the WAL scan.
`AlterFunctionFlags` snapshots the post-mutation state of all four mutable
attributes (`Volatile`/`SecurityDefiner`/`Leakproof`/`Strict`) rather than
a which-clause-changed delta — simpler to replay and always matches what
`execAlterFunction` itself leaves behind (PG's grammar allows combining
multiple attribute clauses in one `ALTER FUNCTION`, so there's no single
"the clause" to encode).

`catalog.Routines` gained four recovery mutators
(`internal/catalog/routines.go`): `CreateDuringRecovery` (idempotent,
OID-preserving, returns the stored pointer so the caller can populate
dependency fields on the live object), `DropByOIDDuringRecovery`,
`RenameByOIDDuringRecovery` (thin wrapper composing `LookupByOID` +
`RenameRoutine`), `SetFlagsByOIDDuringRecovery`. All scan `byKey` linearly
by OID — `Routines` has no OID index today (only schema+name+signature and
schema+name), and adding one for a recovery-only, restart-time-only path
wasn't justified.

New driver `internal/initdb/function_ddl_recovery.go`
(`replayFunctionDDLRecords`) mirrors `replayEventTriggerDDLRecords`
structurally: scan `wal.ReadAll`, filter `wal.IsGoopgNativeRecord`, switch
on the kind byte. Wired into `open.go` right after the event-trigger
replay call. Like event triggers, `catalog.Routine.Schema` is a plain
string (no `NamespaceOID` to resolve against the schema registry), so
ordering relative to schema replay doesn't matter.

`executor.extractRoutineDeps` was renamed (exported) to `ExtractRoutineDeps`
via `mcp__serena__rename_symbol` (one call site updated automatically) so
the recovery driver can call it directly — `internal/initdb` already
imports `internal/executor` elsewhere, no cycle. For a replayed
`Language == "sql"` routine, the driver calls `ExtractRoutineDeps` on the
stored routine after registration, recomputing `SequenceDeps`/
`RoutineCallOIDs`/`TableDeps`/`ColumnDeps` from `Body`/`ArgDefaults` the
same way the live `CREATE FUNCTION` path derives them at creation time.
These four fields are deliberately absent from `CreateFunctionPayload` —
they're derived state, not source-of-truth state, so serializing them
would just be a second, driftable copy.

### The DROP FUNCTION transactional-safety analysis (why this loop looked harder than loop #71)

`DROP FUNCTION` has two removal paths plus a CASCADE side-block, and
naively copying the "log at statement-mutation time" convention every
prior WAL-persisted DDL family used would have been unsafe for one of
them:

1. **Autocommit-immediate** (`execDropFunction`'s `else` branch, no
   explicit transaction active): `rs.DropRoutine` runs immediately with no
   rollback-undo tracking. Logging here at mutation time is safe — there's
   nothing to diverge from, matching the event-trigger precedent.
2. **Deferred-to-COMMIT** (explicit transaction active): the actual
   removal doesn't happen until `ApplyDeferredRoutineDrops` runs — and
   that function is called from exactly two places,
   `operators_tx.go`'s `execCommit` and `dispatch.go`'s simple-query
   `TxCommit` case, **both only on a real COMMIT**. A `ROLLBACK` instead
   calls `sess.TakeDeferredRoutineDrops()` directly to discard the pending
   entries — `ApplyDeferredRoutineDrops` never runs. So logging *inside*
   `ApplyDeferredRoutineDrops`, right after the (successful)
   `rs.DropRoutine` call, is inherently rollback-safe: the log call and the
   durable removal are the same conditional, gated by the same single
   chokepoint.
3. **CASCADE-dependent drops** (`execDropFunction`'s
   `s.Behavior == parser.DropCascade` block, which runs unconditionally
   before the autocommit-vs-deferred dispatch): these also have no
   rollback-undo tracking, so logging immediately is safe by the same
   reasoning as (1).

`DROP PROCEDURE` (`execDropProcedure`) was investigated and explicitly
**excluded** from this loop: it mutates the registry immediately
(unconditionally — no explicit-transaction deferral at all, unlike DROP
FUNCTION) but *is* undoable on ROLLBACK, via a different mechanism
(`BasicSession.pendingRoutineDrops`/`AddPendingRoutineDrop`, restored via
`rs.Create(r, true)` at 7 scattered call sites across `dispatch.go`
(5×)/`server.go` (1×)/`twophase.go` (1×), not the single
`ApplyDeferredRoutineDrops` chokepoint DROP FUNCTION funnels through).
Logging at `execDropProcedure`'s immediate-mutation point would reproduce
scenario (2)'s hazard without its safety net: `BEGIN; DROP PROCEDURE
p(); ROLLBACK;` correctly leaves `p` callable in the live session, but a
naive immediate WAL.Append would have already made the drop durable, so a
post-restart server would show `p` gone. See the loop #73 deferral-ledger
row for the two resume-point options (a dedicated commit-time hook mirroring
`ApplyDeferredRoutineDrops`, or unifying `DROP PROCEDURE` onto that same
mechanism — the latter would also fix a pre-existing, unrelated
transactional-visibility bug where a concurrent session sees a
`DROP PROCEDURE`'d name disappear before the dropping transaction commits).

### Live PG-compatible verification

Manual server (`/tmp/goopg-fn-persist-test`, real `psql` from
`postgres/local_install/bin`, `LD_LIBRARY_PATH` pointed at
`postgres/local_install/lib` to work around the prebuilt libpq/psql
version mismatch): `CREATE FUNCTION add_two(a integer, b integer DEFAULT
1) RETURNS integer LANGUAGE sql AS 'select a + b'`, `CREATE FUNCTION
const_fn() RETURNS integer LANGUAGE sql IMMUTABLE AS 'select 42'`,
`CREATE PROCEDURE noop_proc(x integer) LANGUAGE sql AS 'select 1'`,
`CREATE FUNCTION todrop() ...` + `DROP FUNCTION todrop()`, `ALTER FUNCTION
const_fn() RENAME TO const_fn_renamed`, `ALTER FUNCTION
const_fn_renamed() STRICT SECURITY DEFINER`, then `goopg stop` + `goopg
start` against the same data dir. Post-restart `pg_proc` query confirmed
`add_two`/`const_fn_renamed`/`noop_proc` present with correct
`prosrc`/`provolatile`/`prosecdef`/`proleakproof`, `todrop` and the old
`const_fn` name both correctly absent; `SELECT const_fn_renamed()` and
`CALL noop_proc(5)` both executed successfully against the recovered
`Body`.

### Tests

`internal/wal/function_ddl_test.go`: encode/decode round trips for all 4
kinds, including a numeric(10,2) typmod arg, empty-args/all-flags-set,
OUT/INOUT/VARIADIC procedure args, and multi-byte UTF-8 schema/name/body;
wrong-kind and truncated-payload rejection for all 4 decoders.
`internal/initdb/function_ddl_recovery_test.go`: 6 tests — real
`Init`/`Open`/`WAL.Append`/`Close`/re-`Open` round trips for a plain
CREATE (asserting the arg list, numeric typmod, and that a post-replay
`Create` call allocates an OID past the replayed one), CREATE OR REPLACE
(a second CREATE record for the same schema.name(sig) overwrites in place,
preserving the OID, rather than duplicating the registry entry), CREATE +
DROP, CREATE + RENAME + FLAGS, plus missing-WAL-dir/nil-catalog no-op
guards.

### Gates

`go build ./...`/`go vet ./...` clean; `internal/wal`+`internal/catalog`+
`internal/executor`+`internal/initdb`+`internal/planner`+`internal/parser`+
`internal/server` suites PASS; `TestPort_PgDumpConnectionSetup` PASS (no
regression); TPC-H spotcheck Q12=2/Q13=33 PASS; full pre-commit gate incl.
pgbench TPC-B smoke (0 failed transactions across standard/simple-update/
select-only) PASS.

### Still deferred

`DROP PROCEDURE` WAL persistence — see the "transactional-safety analysis"
section above and the loop #73 deferral-ledger row for the resume point.
Everything else in the `CREATE`/`ALTER`/`DROP FUNCTION` and
`CREATE`/`ALTER PROCEDURE` surface goopg already supports at the SQL level
is now WAL-persisted.
