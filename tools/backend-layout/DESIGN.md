# Design — aligning `internal/` with `postgres/src/backend`

## 1. Goal

goopg's `internal/` tree (~38 packages, ~390K LOC) is flat and uses
goopg-coined names (`planner`, `pgnodes`, `mvcc`, `wal`, `sqlstate`, `mctx`,
`server`, `protocol`, `pgdatetime`, `pgarray`, …). PostgreSQL's equivalent code
lives in a nested `src/backend/` tree (`optimizer/`, `nodes/`, `access/transam/`,
`storage/{aio,lmgr}`, `utils/{adt,mb,mmgr,misc,errcodes.h}`, `commands/`, `libpq/`,
`postmaster/`).

This tool relocates goopg packages so the directory tree mirrors `src/backend/`
as closely as is practical **without redesigning the dependency graph**. The
`tmp/dir-compat/` documents (goopg-dep-analyzer phase 1 & 2) describe a
graph-guided reorganization; this tool realizes the narrower, mechanical subset
of that vision: *pure package relocations* (directory move + package rename +
import-path rewrite + selector-ident rename).

## 2. The mapping

| old path | old pkg | new path | new pkg | PG counterpart |
|---|---|---|---|---|
| `internal/planner` | `planner` | `internal/optimizer` | `optimizer` | `optimizer/` |
| `internal/pgnodes` | `pgnodes` | `internal/nodes` | `nodes` | `nodes/` |
| `internal/access/btree` | `btree` | `internal/access/nbtree` | `nbtree` | `access/nbtree/` |
| `internal/aio` | `aio` | `internal/storage/aio` | `aio` | `storage/aio/` |
| `internal/mctx` | `mctx` | `internal/utils/mmgr` | `mmgr` | `utils/mmgr/` |
| `internal/mb` | `mb` | `internal/utils/mb` | `mb` | `utils/mb/` |
| `internal/mvcc` | `mvcc` | `internal/access/transam` | `transam` | `access/transam/` |
| `internal/multixact` | `multixact` | `internal/access/transam/multixact` | `multixact` | `access/transam/multixact.c` |
| `internal/wal` | `wal` | `internal/access/transam/xlog` | `xlog` | `access/transam/xlog/` |
| `internal/vacuum` | `vacuum` | `internal/commands/vacuum` | `vacuum` | `commands/vacuum.c` |
| `internal/autovacuum` | `autovacuum` | `internal/postmaster/autovacuum` | `autovacuum` | `postmaster/autovacuum.c` |
| `internal/sqlstate` | `sqlstate` | `internal/utils/errcodes` | `errcodes` | `utils/errcodes.h` |
| `internal/server` | `server` | `internal/postmaster` | `postmaster` | `postmaster/` |
| `internal/protocol` | `protocol` | `internal/libpq` | `libpq` | `libpq/` |
| `internal/auth` | `auth` | `internal/libpq/auth` | `auth` | `libpq/auth.c` |
| `internal/lockmgr` | `lockmgr` | `internal/storage/lmgr` | `lmgr` | `storage/lmgr/` |
| `internal/pgdatetime` | `pgdatetime` | `internal/utils/adt/datetime` | `datetime` | `utils/adt/datetime.c` |
| `internal/pgarray` | `pgarray` | `internal/utils/adt/array` | `array` | `utils/adt/arrayfuncs.c` |
| `internal/sqlkeywords` | `sqlkeywords` | `internal/parser/sqlkeywords` | `sqlkeywords` | `parser/kwlist` |
| `internal/config` | `config` | `internal/utils/misc` | `misc` | `utils/misc/` (guc.c) |

Thirteen rows rename the package (identifier churn); seven are nest-only (`aio`,
`mb`, `multixact`, `vacuum`, `autovacuum`, `auth`, `sqlkeywords`) and keep their
package name.

## 3. Intentionally not moved

| package | reason |
|---|---|
| `catalog`, `executor`, `parser`, `storage`, `access` | already match PG top-level names |
| `initdb` | PG lives at `src/bin/initdb`, not backend |
| `plpgsql` | `src/pl/plpgsql` |
| `amcheck` | `contrib/amcheck` |
| `pglz` | `src/common/pg_lzcompress.c` |
| `gls`, `hashsize`, `runtimeshim`, `estimateaudit`, `stats`, `activity`, `control`, `pgtemp`, `lockwait`, `analyzer` | goopg-specific shims or ambiguous PG mapping |
| `testport`, `testutil` | test infrastructure (`src/test/`); no consolidation needed |

## 4. Safety argument — no import cycles

Every relocation renames one node of the module's import graph; the edge set is
unchanged (an importer still imports the same package, now under a new label).
Renaming nodes of a DAG yields a DAG, so **no import cycle can be introduced**.
The only operations that could create a cycle — merging two packages or moving a
symbol between packages — are explicitly out of scope.

## 5. Edit computation

Two passes, both keyed by absolute file path:

1. **Syntactic pass** (filesystem walk of every `.go` file, dot-directories and
   the tool's own directory excluded):
   - import path string literal `"…/internal/<old>"` → `"…/internal/<new>"`;
   - package clause rename for files inside a moved directory, preserving the
     external-test `_test` suffix (`package planner_test` → `package
     optimizer_test`).

2. **Type-aware pass** (`golang.org/x/tools/go/packages`, `Tests: true`):
   selector renames via `TypesInfo.Uses`. For a selector `pkg.Sel`, if `Uses[pkg]`
   is a `*types.PkgName` whose `Imported().Path()` is a moved package **and** whose
   local `Name()` equals the old package name, the ident is renamed. This means a
   shadowing local (`config := …; config.field`) is never touched, and an explicit
   alias (`import cfg "…/config"`) keeps its alias.

Files whose package fails to type-check fall back to a syntactic selector rename
and are reported; the build is the final check.

Edits are applied as byte-range replacements (ascending offsets) — the file is
never re-printed, so go1.25 formatting is byte-preserved (the repo must not be
`gofmt -w`-ed with a newer toolchain).

## 6. Known risks and mitigations

- **Generic new names** (`datetime`, `array`, `nodes`, `misc`, `lmgr`) can collide
  with a local identifier in an importing file. The type-aware pass avoids
  *mis-renames*, but a *new* package name can still shadow an existing local. The
  Go compiler surfaces such cases at `go build`; the fix is to rename the local
  identifier (or, if the package name must win, alias the import). Exactly one
  instance materialized in this apply: `internal/initdb/catalog_heap_reload.go`
  had a local `nodes` variable (a `[]nodes.Node` list) that collided with the
  renamed `nodes` package — fixed by renaming the local to `nlist`. The old
  `pg`-prefixed names existed precisely to avoid such collisions.
- **`//go:generate` / `//go:embed`** inside moved files: embeds are relative to the
  file and move with it; generate commands that reference a path are checked by the
  post-apply grep sweep.
- **Hardcoded path literals** in build/CI/generators are not Go imports and are
  updated by hand (see README "Hardcoded references").

## 7. Verification

1. `go build ./...` — catches any missed import-path rewrite.
2. `go vet ./...`.
3. `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — the mandatory
   unit/component bar.
4. Command smoke: `go build -o bin/goopg ./cmd/goopg` + `--version`; rebuild the
   generators (`cmd/gen-sqlstate`, `cmd/gen-planner-flag-labels`) and confirm
   byte-identical output to the new paths.
5. `scripts/tpch-spotcheck.sh` — recommended end-to-end smoke (a pure rename cannot
   change row counts, but it exercises the full runtime graph).

## 8. Move record

`moves.tsv` (generated by `plan` and `apply`) records every `old path → new path`
at file granularity; `mapping.tsv` is the package-level source of truth. Both are
committed so the relocation is auditable without chasing git history.
