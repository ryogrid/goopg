# 0095-0003 — In-place tablespace foundation (CREATE/DROP TABLESPACE)

**Status:** foundational slice landed (GUC + DDL + in-place directory +
runtime registry). BASE_BACKUP per-tablespace tar emission and on-disk
`pg_tablespace` heap visibility deferred (see Deferral).
**Milestone:** M0095-0003 (`011_in_place_tablespace.pl`).
**Related:** [0095-0003-pg-basebackup-execution.md](0095-0003-pg-basebackup-execution.md)
(physical BASE_BACKUP streaming, already accepted).

## Why this doc exists

`011_in_place_tablespace.pl` was long blocked behind a (now-corrected) note that
blamed BASE_BACKUP. The deferral ledger (2026-06-14, loop #28) re-scoped it: the
real gap is the **in-place tablespace feature**, which decomposes into three
independent pieces:

1. the `allow_in_place_tablespaces` GUC,
2. `CREATE TABLESPACE name LOCATION ''` DDL (statement + directory + catalog),
3. BASE_BACKUP emitting each non-default tablespace as a separate `<oid>.tar`.

This slice lands (1) and the executable core of (2). It is the first
committable, fully-unit-testable layer, mirroring the engine-first pattern used
throughout M0095/M0102/M0110.

## What landed

| layer | change | file |
|-------|--------|------|
| GUC | `allow_in_place_tablespaces` (TypeBool, boot off, `ContextSuset`, session/txn scope), plus a commented `postgresql.conf.sample` entry under a new DEVELOPER OPTIONS section. Mirrors `guc_tables.c:1949` (PGC_SUSET, GUC_NOT_IN_SAMPLE). | `internal/config/defaults.go`, `internal/config/postgresql.conf.sample` |
| AST | `CreateTablespaceStmt{Name, Owner, Location, Options}`, `DropTablespaceStmt{IfExists, Name}` | `internal/parser/ast.go` |
| Parser | `CREATE TABLESPACE name [OWNER [=] role] LOCATION 'dir' [WITH (opts)]` (`parseCreateTablespaceTail`) + `DROP TABLESPACE [IF EXISTS] name`. `tablespace` is an unreserved keyword (`KwTablespace`), so dispatch uses `acceptKeyword`, not `acceptIdentKeyword`. | `internal/parser/ddl.go` |
| Planner | both statements added to the `DDL` passthrough case list | `internal/planner/planner.go` |
| Executor | `execCreateTablespace` / `execDropTablespace`: validate, allocate OID via the registry, create/remove `pg_tblspc/<oid>` | `internal/executor/operators_ddl.go` |
| Catalog | runtime in-place tablespace registry (`tablespaces map[string]*tablespaceRow`, `CreateTablespace`/`DropTablespace`) | `internal/catalog/catalog.go` |
| Wire | `CREATE TABLESPACE` / `DROP TABLESPACE` command tags | `internal/server/dispatch.go` |

## Semantics (faithful to `commands/tablespace.c`)

`CreateTableSpace` (`tablespace.c:207`) and `create_tablespace_directories`
(`:572`) define the behavior goopg mirrors:

- `in_place = allow_in_place_tablespaces && len(location) == 0`.
- A location containing `'` → `42602` "tablespace location cannot contain single
  quotes" (CREATE-DATABASE safety).
- `!in_place && !is_absolute_path(location)` → `42P17` "tablespace location must
  be an absolute path". This is also the path an **empty** `LOCATION` takes when
  the GUC is off — exactly as upstream.
- A reserved `pg_`-prefixed name → `42939` "unacceptable tablespace name …" with
  detail "The prefix \"pg_\" is reserved for system tablespaces."
- A duplicate name → `42710` "tablespace … already exists".
- For an in-place tablespace, `create_tablespace_directories` makes
  `pg_tblspc/<oid>` a **real directory** (not a symlink). goopg creates exactly
  that under `Context.DataDir`.

**Intentional divergence:** an *absolute external* `LOCATION` is valid in PG but
goopg cannot relocate relation files into an arbitrary directory, so it raises
`0A000` "tablespaces with an external location are not supported" with a hint to
use the in-place form. Only the in-place form is meaningful in goopg today.

`DROP TABLESPACE` removes the registry entry and `RemoveAll`s the in-place
directory; a missing name without `IF EXISTS` raises `42704` "tablespace … does
not exist".

When `Context.DataDir` is empty (embedded/test contexts with no cluster on
disk), the registry entry stands alone and no filesystem effect occurs —
matching how other DDL operators skip cluster-filesystem side effects.

## Catalog-visibility scope decision

`pg_tablespace` in goopg is a **bootstrapped on-disk heap relation**
(`internal/initdb/pg_tablespace_bootstrap.go`), not a virtual table with a
runtime override hook like `pg_extension`. Making a runtime-created tablespace
appear in `pg_tablespace` therefore requires either a new per-relation virtual
overlay or a write into a *shared* on-disk catalog — the latter is a separate
hard capability goopg lacks (no runtime shared-catalog `RelFileNode` resolver;
see the `goopg_no_runtime_shared_catalog_inplace_update` lesson). This slice
deliberately scopes that out: the runtime registry tracks created tablespaces
(for duplicate rejection and OID→directory mapping), and the verifiable artifact
is the in-place directory. `CREATE TABLE … TABLESPACE foo` already ignores the
TABLESPACE clause, so the missing catalog row is not yet observable by any
supported query path.

## Tests

- `parser.TestParseCreateTablespace` / `TestParseCreateTablespaceMissingLocation`
  / `TestParseDropTablespace`.
- `catalog.TestCreateTablespaceRegistry` (OID allocation, case-insensitive
  duplicate, drop returns OID, name reuse after drop).
- `executor.TestCreateInPlaceTablespace` (real temp data dir: one `pg_tblspc/<oid>`
  dir created, removed on DROP), `…Duplicate` (42710), `…DropTablespaceMissing`
  (42704 / IF EXISTS), `…GUCOff` (42P17), `…ReservedName` (42939),
  `…ExternalLocation` (0A000), `…QuoteInLocation` (42602).
- `config.TestAllowInPlaceTablespacesGUC` (boot off, `ContextSuset`, settable);
  `config.TestSampleConfigCoversRegistry` covers the new sample entry.

## Deferral

Two pieces remain for `011_in_place_tablespace.pl` to pass:

1. **`pg_tblspc/<oid>/PG_18_<catversion>` version subdir** — `create_tablespace_-
   directories` creates it eagerly (PG also creates it lazily via
   `TablespaceCreateDbspace`). Needs the catversion string (single source of
   truth in `internal/initdb`); land it alongside the BASE_BACKUP work, which
   needs the same string.
2. **BASE_BACKUP per-tablespace `<oid>.tar`** — `basebackup.c` ships each
   non-default tablespace as its own tar with the tablespace path in the
   `BASE_BACKUP` tablespace list. `internal/server/basebackup.go` must enumerate
   the in-place tablespaces and emit one tar each; `pg_basebackup -D … -T`
   relocation maps them on restore.

`011` self-skips on (2) until both land. On-disk `pg_tablespace` heap visibility
is a further, independent capability (shared-catalog runtime write) tracked
separately.
