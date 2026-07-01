# CREATE OPERATOR round-trip in pg_dump (DU-002 slice 406/407)

- **Milestone/Spec:** M0119-0004 (pg_dump 002–010 TAP / DU-002 catalog-view parity battery)
- **Status:** accepted
- **Loop:** #30 (slice 406, verifying/landing work started by a prior
  backgrounded loop); #32 (slice 407, COMMUTATOR/NEGATOR/RESTRICT/JOIN/
  MERGES/HASHES + unary operators); #33 (`ALTER OPERATOR ... SET (...)`,
  closing the slice-407 ledger follow-up); #34 (`CREATE OPERATOR FAMILY`,
  slice 408); #35 (`CREATE OPERATOR CLASS` pg_opclass population, slice 409)

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
