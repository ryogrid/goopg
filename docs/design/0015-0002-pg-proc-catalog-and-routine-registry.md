# 0015-0002 — pg_proc Catalog and Routine Registry

**Status:** accepted (step 2 — registry + virtual view; analyzer /
executor wiring deferred to step 3)
**Milestone:** [0015 — PL/pgSQL Stored Routines (Function-First Delivery)](../milestones/0015-plpgsql-stored-routines-function-first.md)
**Spans seam:** catalog routine bookkeeping, `pg_catalog.pg_proc`
introspection.
**Cross-links:**
[0015-0001](0015-0001-create-function-parser-and-ast.md)
(parser surface), [0010-0003](0010-0003-wal-direct-io-observability-and-operations.md)
(virtual-view conventions).

## Context

M0015-0001 step 1 added the parser surface for `CREATE [OR REPLACE]
FUNCTION` and `DROP FUNCTION`. The analyzer rejects both for now
(SQLSTATE `0A000`). Before the analyzer / executor wiring can land,
the catalog needs a place to hold routine metadata — a registry
the future `analyzeCreateFunction` / `executeCreateFunction` /
function-resolver paths can call into.

This step is **catalog-only**: a new `Routines` registry that owns
the in-memory routine map, plus the upstream-flavoured
`pg_catalog.pg_proc` virtual view that renders one row per
registered routine. The registry has no caller yet — analyzer /
executor / replication adoption is step 3+.

Decoupling the registry now lets step 3 land as a small wiring
slice without redesigning the catalog's relation-key map.

## Why a separate registry, not a virtual table

Functions don't have a relfilenode — they're catalog metadata, not
heap data. Folding them into `*InMemory.tables` would either:

- Pollute the table-name keyspace (a function and a table can share
  a name in upstream — we'd lose that), or
- Require a `kind` discriminator on every catalog map entry that
  the existing CRUD ignores.

A separate registry keeps existing table CRUD untouched and gives
the future overload resolver a clean home (signature-keyed map
with a name-keyed secondary index).

## API

```go
type Routine struct {
    OID        uint32
    Schema     string   // defaults to "public" when callers leave empty
    Name       string
    ArgNames   []string // parallel to ArgTypes; empty for positional-only args
    ArgTypes   []Type
    ReturnType Type
    Language   string   // lower-cased
    Body       string   // raw routine source
}

func (r *Routine) QualifiedName() string
func (r *Routine) Signature() string  // "(int,text,...)"

type Routines struct { /* RWMutex-guarded maps */ }

func NewRoutines() *Routines
func (rs *Routines) Create(r *Routine, orReplace bool) (*Routine, error)
func (rs *Routines) Drop(name parser.ObjectName, argTypes []Type) error
func (rs *Routines) DropByName(name parser.ObjectName) error  // ambiguity-aware
func (rs *Routines) Lookup(name parser.ObjectName, argTypes []Type) (*Routine, bool)
func (rs *Routines) LookupByName(name parser.ObjectName) []*Routine
func (rs *Routines) List() []*Routine  // OID-ordered
```

Errors are typed for callers' SQLSTATE mapping:

- `ErrRoutineExists` — Create with `orReplace=false` and
  signature collision (SQLSTATE 42723 "duplicate function").
- `ErrRoutineNotFound` — Drop / Lookup against a missing
  signature (SQLSTATE 42883 "undefined function").
- `ErrRoutineAmbiguous` — DropByName with > 1 overload
  (SQLSTATE 42725 "ambiguous function").

## OID space

`FirstRoutineOID = 1 << 17 = 131072` — well above
`FirstUserOID = 16384` so routine OIDs never collide with the
table-OID space goopg already uses. Assignment is monotonic; CREATE
OR REPLACE preserves the existing OID so external references stay
valid.

## Schema defaulting

Callers that omit the schema land routines under `public`. Mirrors
upstream's default `search_path[0]` for routine creation when the
session hasn't switched namespaces. Lookups follow the same rule.

## `pg_catalog.pg_proc` virtual view

Columns mirror the upstream pg_proc shape used by `\df` and
pg_dump:

| column         | source                          |
|----------------|---------------------------------|
| `oid`          | `Routine.OID` formatted as text |
| `proname`      | `Routine.Name`                  |
| `pronamespace` | `Routine.Schema`                |
| `prolang`      | `Routine.Language`              |
| `prorettype`   | `Routine.ReturnType.Name`       |
| `proargtypes`  | comma-joined argument-type names|
| `prosrc`       | `Routine.Body`                  |

Stage A doesn't yet emit upstream's `oidvector` / numerical-type-OID
columns; once goopg's type system grows numeric type OIDs we swap
the type-name columns to match upstream byte-for-byte. The
text-OID convention matches `pg_class` / `pg_indexes` already in
the codebase.

The view is registered from `internal/initdb/Open` immediately
after `pg_stat_wal_io` so a `\df` against a freshly-opened cluster
shows zero rows instead of a missing-table error.

## Tests

`internal/catalog/routines_test.go`:

- `TestRoutinesCreateAssignsOID` — OID monotonicity from
  `FirstRoutineOID`.
- `TestRoutinesCreateRejectsDuplicate` — `errors.Is(_,
  ErrRoutineExists)`.
- `TestRoutinesCreateOrReplacePreservesOID` — upstream's contract.
- `TestRoutinesOverloadedNamesDistinct` — same name, different
  argtypes resolves separately.
- `TestRoutinesDropRemovesEntry` — Lookup misses after Drop;
  re-Drop returns `ErrRoutineNotFound`.
- `TestRoutinesDropByNameAmbiguous` — bare-name DROP with > 1
  overload returns `ErrRoutineAmbiguous`.
- `TestRoutinesDefaultSchemaIsPublic` — schema-less Create lands
  routines in `public`; schema-less Lookup resolves there.
- `TestRoutinesListIsOIDOrdered` — pin the ordering the view
  depends on.

`internal/initdb/pg_proc_view_test.go`:

- `TestPgProcViewEmptyByDefault` — view present, zero rows on a
  fresh catalog (the standard "view exists, empty" contract).
- `TestPgProcViewRendersRoutine` — every column maps from the
  routine struct as expected (two-arg function with body).
- `TestPgProcViewOrdering` — view rows in OID order.

Full `go test ./...` green.

## Out of scope (step 2)

- Analyzer wiring for `CreateFunctionStmt` / `DropFunctionStmt` —
  step 3. Today the analyzer still rejects with SQLSTATE `0A000`.
- Executor `CreateFunction` / `DropFunction` operators — step 3.
  No statement currently calls into the registry; CRUD is
  test-only API for now.
- Persistence (durable `pg_proc` rows surviving restart) — pairs
  with the broader on-disk catalog work in M0007+ scope.
- PL/pgSQL parser + AST for routine bodies — step 4.
- PL/pgSQL interpreter and SPI bridge — step 5.
- Function invocation in expression contexts (the FuncCall
  resolver path) — step 6.
- Numerical type-OID columns in `pg_proc` — paired with the wider
  type-system work that swaps every text-OID surface to integer.

## `ALTER FUNCTION/PROCEDURE/ROUTINE OWNER TO` / `SET SCHEMA` (2026-07-08, M0097-0150)

Closes the last two named forms of the M0097-0150 unimplemented-feature
entry — `RENAME TO` already worked (loop #71 ledger follow-up); `OWNER TO`
and `SET SCHEMA` parsed but were silently discarded no-ops, so `pg_proc`'s
`proowner`/`pronamespace` never reflected an ALTER.

**Catalog:** `Routine` gains `Owner uint32` (0 = unset → bootstrap superuser,
mirrors every other `OwnerOrDefault`-style object in this codebase —
`UserAggregate`, `UserCollation`, `UserOperator`, etc.) and `OwnerOrDefault()`.
`Routines.SetSchema(r, newSchema)` re-keys both `byKey`/`byName` indices
(schema is part of both keys) — mirrors `RenameRoutine`'s re-keying, just on
`Schema` instead of `Name`. `SetSchemaByOIDDuringRecovery`/
`SetOwnerByOIDDuringRecovery` are the WAL-replay counterparts (owner isn't
part of either key, so its recovery path is a direct field write, mirroring
`SetFlagsByOIDDuringRecovery`).

**Parser:** `AlterFunctionStmt` gains `NewOwner`/`NewSchema` fields. A real
pre-existing bug surfaced here: the attribute-consuming loop's SET-clause
detection matched `TokenIdent` with value `"set"`, but `SET` lexes as the
real keyword token `KwSet` — so the condition never matched and **every**
`ALTER FUNCTION ... SET ...` form (not just `SET SCHEMA`) was a syntax
error, not merely the documented no-op. Fixed by matching `TokenKeyword{Keyword:
KwSet}` (and `KwFrom` for the `SET x FROM CURRENT` sub-form) instead of
`TokenIdent`.

**Executor:** `execAlterFunction` gains two early-return branches (mirroring
`RenameTo`'s existing early return — real PostgreSQL grammar treats `OWNER
TO`/`SET SCHEMA`/`RENAME TO` as three separate top-level `AlterFunctionStmt`-
adjacent forms, not clauses combinable with `VOLATILE`/`STRICT`/etc, per
`gram.y`'s `AlterOwnerStmt`/`RenameStmt` vs. `alterfunc_opt_list`):
`OWNER TO` resolves the new owner via `catalog.RoleOID` (42704 if unknown,
`CURRENT_USER`/`SESSION_USER`/`CURRENT_ROLE` resolve to the bootstrap-
superuser sentinel exactly like `execAlterAggregateOwner`/`execAlterCollation`)
and WAL-logs `RecordKindAlterFunctionOwner`; `SET SCHEMA` calls the new
`Routines.SetSchema` and WAL-logs `RecordKindAlterFunctionSetSchema`.

**`pg_proc` rendering:** the user-routine row builder's `proowner` column
was a hardcoded `"10"` — now `r.OwnerOrDefault()`. `pronamespace` already
read `r.Schema` live, so `SET SCHEMA` round-trips with no view change.

**WAL/restart persistence:** new record kinds 121 (`RecordKindAlterFunctionOwner`,
`kind|ownerOID|oid`) and 122 (`RecordKindAlterFunctionSetSchema`,
`kind|oid|newSchemaLen|newSchema`), replayed by `replayFunctionDDLRecords`
(`internal/initdb/function_ddl_recovery.go`).

**Verified against a live `cmd/goopg` binary** (not just unit tests): created
a role and a function, ran `ALTER FUNCTION add_one(int) OWNER TO func_owner`
+ `ALTER FUNCTION add_one(int) SET SCHEMA app`, confirmed `pg_proc.proowner`/
`pronamespace` changed to the real role/schema OIDs and `app.add_one(41)`
still executed correctly — then `goopg restart`'d the same data directory and
confirmed both the ownership and the schema move survived, with the function
still callable under its new schema.

Tests: `TestParseAlterFunctionOwner`/`TestParseAlterFunctionSetSchema`/
`TestParseAlterFunctionRenameAndVolatileStillWork` (parser); `TestExecAlterFunctionOwner`/
`TestExecAlterFunctionSetSchema` (executor); `TestPgProcViewRendersRoutineOwner`
(initdb); `TestEncodeDecodeAlterFunctionOwnerRoundTrip`/
`TestEncodeDecodeAlterFunctionSetSchemaRoundTrip` (wal); `TestFunctionDDLRecoveryReplaysAlterAfterCreate`
extended to cover both new record kinds end-to-end through a real WAL
flush + reopen (initdb).

**Was still open, now closed (2026-07-08 follow-up):** the generic `SET
config_param {TO|=} value` / `SET config_param FROM CURRENT` / `RESET`
clauses on `ALTER FUNCTION` (legitimate PostgreSQL grammar per `gram.y`'s
`common_func_opt_item: FunctionSetResetClause`, combinable with
`VOLATILE`/etc in the same statement, unlike `OWNER TO`/`RENAME TO`/`SET
SCHEMA`) were unreachable dead code before the row above's loop (same
`KwSet`-vs-`TokenIdent` bug) and remained broken after it — that fix only
repaired the `SET SCHEMA` sub-branch inside the block. This follow-up widened
the `=`-acceptance check to also match `TokenOperator` (`=` doesn't lex as
`TokenSymbol` in this lexer) and restructured the branch so the
config-parameter name is always parsed before checking for `TO`/`=`/`FROM
CURRENT`, matching `gram.y`'s actual `var_name {TO|=} var_value | var_name
FROM CURRENT` order (the old code incorrectly checked for a literal `"from"`
token immediately after `SET`, as if `FROM` itself could be the parameter
name). All forms remain parse-only no-ops (goopg has no per-function
GUC-override storage) — same as `RESET` already was. Verified live: all 5
previously-broken forms (`SET x TO v`, `SET x = v`, `SET x FROM CURRENT`,
`SET x TO DEFAULT`, `RESET x`, `RESET ALL`) now return `ALTER FUNCTION`
instead of a syntax error. Test: `TestParseAlterFunctionGenericSetReset`
(parser).

**Was still open, now closed (2026-07-08 follow-up 2):** the comma-separated
`var_list` value form (`SET search_path = app, public`, real PG `gram.y`
grammar) still errored after the follow-up above — the no-op branch only
ever consumed a single value token. Fixed by reusing the same
`p.parseSetValueAtoms()` helper the generic `SET` statement
(`parser.go`'s `parseSet`) already uses for comma-separated GUC values,
discarding the parsed list (still a no-op). Verified live: `SET search_path
= app, public` / `SET search_path TO app, public, pg_catalog` now both
return `ALTER FUNCTION`. Test: two new cases in
`TestParseAlterFunctionGenericSetReset` (parser). **The ALTER
FUNCTION/PROCEDURE/ROUTINE cluster (OWNER TO/RENAME TO/SET SCHEMA/generic
SET-RESET incl. var_list) has no known open residuals.**

**Follow-up (2026-07-08, M0122-0007): the generic SET/RESET clause actually
storing/rendering `pg_proc.proconfig` — DU-002's "Per-function SET
configuration parameters are not tracked" entry.** The two follow-ups above
made `ALTER FUNCTION ... SET/RESET` parse-and-execute without erroring, but
left both halves of the actual feature unbuilt: (a) `CREATE FUNCTION`'s own
`SET` clause (`common_func_opt_item: FunctionSetResetClause`, same production
`ALTER FUNCTION` uses) was not merely a no-op like the doc above implied — it
was a hard **syntax error**, the identical `KwSet`-vs-`TokenIdent` bug fixed
for `ALTER FUNCTION` earlier the same day, just never ported to
`isFunctionAttribute()`/`parseCreateFunctionTail`. Reproduced live via a
throwaway parser probe (`CREATE FUNCTION f() ... SET search_path = public AS
$$...$$` → `"expected AS $$body$$ for CREATE FUNCTION (got set)"`) before
fixing. (b) Every `SET`/`RESET` clause on either statement was still
discarded ("goopg has no per-function GUC-override storage") — there was
nowhere to put it.

Fix, part 1 (parser): a new top-level `case p.cur().Keyword == KwSet` /
`KwReset` in `parseCreateFunctionTail`'s attribute loop
(`internal/parser/function.go`) — sitting alongside the existing
`KwLanguage`/`KwAs`/`KwReturn` cases rather than nested inside
`isFunctionAttribute()`'s `TokenIdent`-keyed sub-switch, since `SET`/`RESET`/
`ALL`/`DEFAULT`/`FROM` all lex as real keyword tokens, never `TokenIdent`.
Two new shared helpers, `parseFunctionConfigSetClause`/
`parseFunctionConfigResetClause`, replace the discard-only logic `ALTER
FUNCTION`'s branch (`internal/parser/ddl.go`) had grown across the two
follow-ups above — both statements now populate a new
`[]FunctionConfigOp{Reset, ResetAll, Name, Value}` field (`ConfigOps` on both
`CreateFunctionStmt` and `AlterFunctionStmt`, `internal/parser/ast.go`) in
statement order. Two more of the same keyword-vs-ident lexing bugs surfaced
and were fixed along the way: `RESET ALL` matched via
`acceptIdentKeyword("all")`, which can never succeed since `ALL` is the
reserved keyword `KwAll`, not `TokenIdent` (silently fell through to "RESET
of a GUC literally named all" instead — harmless before now since `RESET`
was a no-op either way, but load-bearing once `ResetAll` needed to be
distinguished from `Reset{Name:"all"}`); same story for `SET x TO DEFAULT`
via `acceptIdentKeyword("default")` vs the reserved keyword `KwDefault` (it
was falling through to `parseSetValueAtoms`, which happily accepts a bare
keyword token and stored the literal string `"default"` as the value).

Fix, part 2 (catalog + executor): new `catalog.Routine.Config []string`
(the exact `pg_proc.proconfig` "name=value"-entry shape, mirroring
`InMemory.roleSettings`' `ALTER ROLE ... SET` storage) plus a package-level
`catalog.ApplyFunctionConfigOps(cfg []string, ops []parser.FunctionConfigOp)
[]string` — a pure function (no locking; `Routine` field mutation elsewhere
in `execAlterFunction`, e.g. `r.Volatile = *s.Volatile`, is unsynchronized
too) that folds a `SET`/`RESET`/`RESET ALL` op list onto a config array:
`SET` upserts case-insensitively by name, `RESET` removes the named entry,
`RESET ALL` clears everything, later ops for the same name win.
`execCreateFunction` calls it once to build a fresh routine's initial
`Config` (`internal/executor/operators_ddl.go`); `execAlterFunction` folds it
into the *same* per-routine loop that already applies
`VOLATILE`/`SECURITY DEFINER`/`LEAKPROOF`/`STRICT` — **not** a new
early-return branch like `RENAME TO`/`OWNER TO`/`SET SCHEMA` above, because
real `gram.y`'s `alterfunc_opt_list` makes the config clause combinable with
those four attributes in one statement (`ALTER FUNCTION f() STRICT SET
search_path = app` is valid PG grammar), unlike the three mutually-exclusive
top-level rename/owner/schema productions.

`pg_proc` rendering: the user-routine row builder's `proconfig` column
(`internal/initdb/pg_proc_view.go`) was an unconditional `""` (NULL) at every
emission site — now `catalog.RoutineConfigArrayLiteral(r.Config)` for the
one row shape that can ever carry a `Config` (user-defined routines); the
sibling built-in-stub/`catalog.BuiltinProcs()`/user-aggregate row builders
correctly stay `""` (none of those can ever have gone through a `SET`
clause). `RoutineConfigArrayLiteral` is a thin exported wrapper around the
same private `optionsArrayLiteral`/`quoteArrayElement` upstream-`array_out`
quoting rules `pg_class.reloptions`/FDW `OPTIONS` already use, so a value
that itself embeds a comma (e.g. `search_path=app,public`, from a `var_list`
SET) round-trips as a properly quoted single array element instead of being
split on the array delimiter.

WAL/restart persistence: `CreateFunctionPayload` (`internal/wal/recovery.go`)
gained an optional trailing `Config []string` extension block — omitted
entirely when empty, byte-identical to a pre-`Config` record for the
overwhelmingly common case of no `SET` clause (same pattern
`CreateIndexPayload`'s predicate/INCLUDE-column extension established). A
new record kind, `RecordKindAlterFunctionConfig` (123), logs the *whole*
post-mutation `Config` snapshot after an `ALTER FUNCTION ... SET/RESET`
(mirrors `RecordKindAlterFunctionFlags`'s whole-state-replay shape rather
than replaying individual ops) — `EncodeAlterFunctionConfig`/
`DecodeAlterFunctionConfig`, replayed by `replayFunctionDDLRecords`
(`internal/initdb/function_ddl_recovery.go`) via a new
`Routines.ReplaceConfigByOIDDuringRecovery`.

Verified live against the real `cmd/goopg` binary (not just unit tests):
`CREATE FUNCTION f() ... SET search_path = app, public AS $$...$$` and
`ALTER FUNCTION f() SET work_mem = '64MB'` both now succeed (previously the
`CREATE FUNCTION` form was a syntax error); `SELECT proconfig FROM pg_proc
WHERE proname = 'f'` showed the real array; a `goopg restart` preserved it.

Tests (all confirmed non-vacuous via a scoped `git stash` of every
implementation file at once — the new types/fields/functions don't exist
without the fix, so the test files fail to *compile*, the strongest form of
non-vacuousness): `TestParseCreateFunctionSetClause`,
`TestParseAlterFunctionGenericSetReset` (extended with `wantOps`
assertions), `TestParseAlterFunctionSetSchemaDistinctFromConfigSet` (parser,
`alter_function_owner_schema_test.go`); `TestApplyFunctionConfigOps`,
`TestReplaceConfigByOIDDuringRecovery` (catalog, `routines_test.go`);
`TestExecCreateFunctionSetClausePopulatesConfig`,
`TestExecAlterFunctionSetResetConfig` (executor,
`operators_function_test.go`); `TestEncodeDecodeAlterFunctionConfigRoundTrip`,
`TestEncodeCreateFunctionOmitsConfigExtensionWhenEmpty` (wal,
`function_ddl_test.go`); `TestPgProcViewProconfigRendersUserRoutineConfig`
(initdb, `pg_proc_view_test.go`); `TestFunctionDDLRecoveryReplaysCreate`/
`TestFunctionDDLRecoveryReplaysAlterAfterCreate` extended with `Config`
round-trip assertions (initdb, `function_ddl_recovery_test.go`).

Deferred (ledger row, 2026-07-08): goopg does not actually *apply* a
routine's `Config` during its own execution (no push/pop of these GUC values
around a plpgsql/SQL function call) — this closes the DU-002 entry's literal
"not tracked ... report NULL" claim (storage + dump-fidelity), not full
upstream runtime semantics; matches this codebase's existing pattern for
several other proconfig-adjacent features (`n_distinct` planner hints, TOAST
compression metadata, extended-statistics target) that are dump-fidelity
only by explicit design. `unimplemented_feat.json`'s matching `DU-002` entry
flipped to `resolved`.

Gates: `go build ./...`/`go vet ./...` clean; `go test
./internal/parser/... ./internal/catalog/... ./internal/executor/...
./internal/wal/... ./internal/initdb/...` PASS (one confirmed pre-existing,
HEAD-reproducible hang in `TestSeqScanFiresPrefetchesAcrossBlocks`,
unrelated to this change — see ledger); `scripts/tpch-spotcheck.sh` PASS
(Q12=2/Q13=33); `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh`
PASS (0 failed, all 3 workloads).
