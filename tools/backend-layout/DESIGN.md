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

The tool has been run twice. `mapping.tsv` always carries only the round being
applied; recover an earlier round's table and per-file log from git
(`git show 22ed9000:tools/backend-layout/mapping.tsv`, likewise `moves.tsv`).

### Round 2 — 13 packages (current `mapping.tsv`)

Retires the whole of the former §3 "intentionally not moved" list below.

| old path | old pkg | new path | new pkg | PG counterpart |
|---|---|---|---|---|
| `internal/activity` | `activity` | `internal/utils/activity` | `activity` | `utils/activity/` (`backend_status.c`, `wait_event.c`) |
| `internal/stats` | `stats` | `internal/utils/activity/stats` | `stats` | `utils/activity/pgstat*.c` |
| `internal/lockwait` | `lockwait` | `internal/storage/lmgr/lockwait` | `lockwait` | `storage/lmgr/proc.c` (`lock_timeout`) |
| `internal/runtimeshim` | `runtimeshim` | `internal/port/runtimeshim` | `runtimeshim` | `backend/port/pg_sema.c`, `src/port/` |
| `internal/gls` | `gls` | `internal/port/gls` | `gls` | same — build-tag-selected portability layer |
| `internal/hashsize` | `hashsize` | `internal/executor/hashsize` | `hashsize` | `executor/nodeHash.c` (`ExecChooseHashTableSize`) |
| `internal/plpgsql` | `plpgsql` | `internal/pl/plpgsql` | `plpgsql` | `src/pl/plpgsql/` |
| `internal/amcheck` | `amcheck` | `internal/access/amcheck` | `amcheck` | `contrib/amcheck/` (verifies `access/{heap,nbtree}`) |
| `internal/pgtemp` | `pgtemp` | `internal/storage/file` | `file` | `storage/file/fd.c` |
| `internal/estimateaudit` | `estimateaudit` | `internal/testutil/estimateaudit` | `estimateaudit` | none — verification tooling (`src/test/`) |
| `internal/pglz` | `pglz` | `internal/access/common/pglz` | `pglz` | `src/common/pg_lzcompress.c` + `access/common/toast_compression.c` |
| `internal/control` | `control` | `internal/access/transam/control` | `control` | `pg_control.h` + `src/common/controldata_utils.c` |
| `internal/analyzer` | `analyzer` | `internal/parser/analyzer` | `analyzer` | `parser/analyze.c` (parse analysis, NOT `commands/analyze.c`) |

One row renames the package (`pgtemp` → `file`, 12 selector sites); the other
twelve are nest-only.

Two judgement calls worth recording. `hashsize` lands under `executor/` even
though `internal/optimizer` imports it: in Go a directory is not a dependency,
so `optimizer → executor/hashsize` creates no edge to `internal/executor`, and
the package itself imports neither parent (its doc comment states the
invariant). `control` moves whole even though it carries two concerns — the
`pg_control` port and the `postmaster.pid` / control-socket plane; splitting it
is a package split, not a relocation, and is therefore out of this tool's scope
(see the deferral ledger).

### Round 1 — 20 packages (applied in bf6767e8)

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

Round 1 left thirteen packages here as "goopg-specific shims or ambiguous PG
mapping". Round 2 resolved every one of them — see §2 — so the list below is
now only what genuinely stays put.

| package | reason |
|---|---|
| `catalog`, `executor`, `parser`, `storage`, `access` | already match PG top-level names |
| `initdb` | PG lives at `src/bin/initdb`, not backend |
| `nodes`, `optimizer`, `postmaster`, `libpq`, `commands`, `utils`, `pl`, `port` | placed by round 1 or round 2; already at their PG name |
| `testport`, `testutil` | test infrastructure (`src/test/`); no consolidation needed |

Still open, but **not** relocations — each needs a package split, which this
tool deliberately cannot express:

| item | what it needs |
|---|---|
| `internal/access/amcheck/bloomfilter.go` | extract to `internal/lib/bloomfilter` (PG: `src/backend/lib/bloomfilter.c`; amcheck merely borrows it) |
| `internal/access/transam/control` | split the `postmaster.pid` / control-socket half out to `internal/postmaster/control` |

`internal/backup` and `internal/replication` were on this list and have now been
carved out of `internal/postmaster` **by hand** (PG: `src/backend/backup/` and
`src/backend/replication/`). They are the worked example of why this tool cannot
express a split: three of the moved files defined methods on `*postmaster.Server`,
and Go pins a method to its receiver's package, so the move required converting
those receivers to a `Handler` type carrying a narrow `Config` plus a
`WriteQueryErrorFunc` callback. `mapping.tsv` / `moves.tsv` were deliberately
NOT touched — those tables are the per-round record of *relocations*, and this
was not one.

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
- **Build-tag-excluded files are invisible to the type-aware pass — silently.**
  The two passes have asymmetric coverage. `listGoFiles` + `syntacticEdits`
  walks the *filesystem*, so it is tag-blind and rewrites import paths and
  package clauses in every `.go` file. `typeAwareEdits` uses `packages.Load`,
  which honours the *current* build context, so a file excluded by its
  `//go:build` line is never loaded — it gets zero selector renames, and
  because `packages.Load` never yields it, it is never added to `unresolved`
  either, so it does **not** appear in the `WARNING: N files needed selector
  renames but had no type info` report. It is a silent hole, not a reported
  one. Concretely: under go1.26.3 the fallback files in `internal/port/gls`
  and `internal/port/runtimeshim` (`//go:build !go1.24 || go1.27 ||
  noLinkname`) are invisible. Round 2 was unaffected — none of them import a
  relocated package — but that was luck, not design. Mitigation is one command
  per relevant tag set, e.g. `go build -tags noLinkname ./...`; treat it as
  mandatory whenever a moved package could be referenced from a tagged file.

## 7. Verification

1. `go build ./...` — catches any missed import-path rewrite.
2. `go build -tags noLinkname ./...` and `go vet -tags noLinkname ./...` — the
   only pass that exercises the build-tag-excluded files §6 describes. Not
   optional.
3. `go vet ./...`.
4. `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — the mandatory
   unit/component bar.
5. Command smoke: `go build -o bin/goopg ./cmd/goopg` + `--version`; rebuild the
   generators (`cmd/gen-sqlstate`, `cmd/gen-planner-flag-labels`) and confirm
   byte-identical output to the new paths.
6. Anything the round touched by hand needs its own check — round 2 edited
   `scripts/runtimeshim_go_matrix.sh` and the `ci/batch/lib` fixture, so
   `make runtimeshim-matrix` and
   `(cd ci/batch/lib && python3 -m unittest test_summarize)` were both run.
7. `scripts/tpch-spotcheck.sh` — recommended end-to-end smoke (a pure rename cannot
   change row counts, but it exercises the full runtime graph).

## 8. Move record

`moves.tsv` (generated by `apply`; `plan` does **not** write it) records every
`old path → new path` at file granularity; `mapping.tsv` is the package-level
source of truth. Both are committed so the relocation is auditable without
chasing git history.

Both files carry only the most recent round. `mapping.tsv` is *input*, and a
stale row whose `old_dir` no longer exists makes `apply` fail in
`writeMoveLog`, so each round replaces the table rather than appending to it.
Earlier rounds live in git:

| round | packages | commit | recover with |
|---|---|---|---|
| 1 | 20 | `bf6767e8` | `git show 22ed9000:tools/backend-layout/{mapping,moves}.tsv` |
| 2 | 13 | this commit | current working tree |
