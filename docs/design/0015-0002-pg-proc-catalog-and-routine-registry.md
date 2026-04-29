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
