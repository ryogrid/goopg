# backend-layout

A single-purpose refactoring tool that relocates goopg packages so the
`internal/` directory tree mirrors PostgreSQL's `src/backend/` layout. It is
driven entirely by `mapping.tsv`, so the only human judgment required is the
mapping table itself — the per-file mechanics are automated.

## What it does

For each row in `mapping.tsv` it performs a **pure package relocation**:

1. moves the directory (filesystem `rename(2)`, no git staging),
2. renames the `package` declaration (and its external-test twin `<pkg>_test`),
3. rewrites every import path string,
4. renames selector idents `oldpkg.X` → `newpkg.X` wherever the import used the
   package's default (unaliased) name.

No package is split or merged and no symbol moves between packages, so the Go
import graph is only relabelled and can never gain a cycle.

## Usage

```bash
cd tools/backend-layout
go build -o backend-layout .

# dry run — report moves and edits, write tools/backend-layout/moves.tsv
./backend-layout -root ../.. plan

# apply — rewrite files, then move directories, write moves.tsv
./backend-layout -root ../.. apply
```

Flags: `-root` (module root, defaults to `.`), `-module` (module path, defaults
to `github.com/goopg/goopg`), `-mapping` (path to the TSV, defaults to
`mapping.tsv`).

## Files

| file | purpose |
|---|---|
| `mapping.tsv` | the single source of truth: `old_dir old_pkg new_dir new_pkg` |
| `main.go` | CLI: `plan` / `apply` |
| `mapping.go` | mapping loader |
| `edits.go` | edit computation (syntactic + type-aware) |
| `move.go` | directory relocation + `moves.tsv` |
| `moves.tsv` | generated record of every `old path → new path` |
| `DESIGN.md` | full design and rationale |

## Safety properties

- **No import cycles.** Relabelling the nodes of an acyclic graph is acyclic.
- **No shadowing bug.** Selector renames use `go/types` (`TypesInfo.Uses`) so a
  local variable named `config`, `server`, `mctx`, etc. is never touched; only a
  `*types.PkgName` bound to a moved package is renamed.
- **No accidental reformat.** Edits are surgical byte-range replacements (exact
  import-path literal, package-clause ident, selector `X` ident). `gofmt -w` is
  never run, so the go1.25 formatting baseline is preserved.
- **Foreign trees untouched.** Dot-directories (`.claude/worktrees`, `.ralph`,
  `.git`, …) and the tool's own directory are excluded from the walk.
- **Aliased / dot / blank imports** are handled: only the import path is
  rewritten; the local alias is left in place.

## Hardcoded references (edited by hand, not by this tool)

The tool rewrites Go imports only. Build/CI/generator scripts that embed an
`internal/<pkg>` path as a string literal are updated separately:

- `cmd/gen-sqlstate/main.go` — default `-out` path
- `Makefile` — `RACE_EXCLUDE`
- `scripts/ralph-precommit-test.sh` — `EXCLUDE`
- `ci/batch/stages/stage-units.sh` — `EXCLUDE` (nightly units stage)
- `scripts/gen-parity-dashboard.sh` — `GOOPG_GUC_SRC`, `GOOPG_SQLSTATE_SRC`
- `.github/workflows/test.yml` — `EXCLUDE`

All four `EXCLUDE`/`RACE_EXCLUDE` regexes carry the same `internal/server`
alternation and must be updated to `internal/postmaster` together, or the
cluster-backed postmaster tests silently start running in the wrong stages.
