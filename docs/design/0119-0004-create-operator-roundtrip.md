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
