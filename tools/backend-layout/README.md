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

# dry run — report moves and edit counts; writes nothing
go run . -root ../.. plan

# apply — rewrite files, write moves.tsv, then move directories
go run . -root ../.. apply
```

Prefer `go run` over `go build -o backend-layout .`: a built binary in this
directory is untracked and lands in a careless `git add` (it happened once, and
`5d4d357e` had to remove it).

Flags: `-root` (module root, defaults to `.`), `-module` (module path, defaults
to `github.com/goopg/goopg`), `-mapping` (path to the TSV, defaults to
`mapping.tsv`).

`mapping.tsv` holds **only the round being applied**. It is input, and a stale
row whose `old_dir` no longer exists makes `apply` fail in `writeMoveLog`, so
replace the table for each round instead of appending — see DESIGN.md §8 for
how to recover an earlier round's table from git.

Read the `plan` output before applying; the edit counts are the cheapest signal
that the mapping is right. A relocation count that matches but a near-zero
import-path count means a typo matched nothing, and a
`WARNING: N files needed selector renames but had no type info` block means
type-checking failed and every selector rename fell back to the imprecise
syntactic path — stop rather than apply.

## Files

| file | purpose |
|---|---|
| `mapping.tsv` | the single source of truth: `old_dir old_pkg new_dir new_pkg` |
| `main.go` | CLI: `plan` / `apply` |
| `mapping.go` | mapping loader |
| `edits.go` | edit computation (syntactic + type-aware) |
| `move.go` | directory relocation + `moves.tsv` |
| `moves.tsv` | record of every `old path → new path`, written by `apply` only |
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

The tool rewrites Go imports, package clauses and selectors — nothing else. It
never edits comments or string literals, so after every round grep the
non-Go tree for the old package names and fix what you find by hand.

Sites that have needed it so far:

| round | file | what |
|---|---|---|
| 1 | `cmd/gen-sqlstate/main.go` | default `-out` path |
| 1 | `scripts/gen-parity-dashboard.sh` | `GOOPG_GUC_SRC`, `GOOPG_SQLSTATE_SRC` |
| 1 | `Makefile`, `scripts/ralph-precommit-test.sh`, `ci/batch/stages/stage-units.sh`, `.github/workflows/test.yml` | the four `EXCLUDE` / `RACE_EXCLUDE` regexes |
| 2 | `Makefile` (help text + comment), `scripts/runtimeshim_go_matrix.sh` | `internal/runtimeshim` → `internal/port/runtimeshim`; two of the script's sites are **executable** `go test` targets |
| 2 | `ci/batch/lib/summarize.py`, `ci/batch/lib/test_summarize.py` | `internal/amcheck` fixture strings — self-consistent (one site is the synthetic input, the rest are its expectations), so change all or none |

The four `EXCLUDE`/`RACE_EXCLUDE` regexes must always be updated **together**,
or tests silently start running in the wrong stages — round 1's
`internal/server` → `internal/postmaster` rename is the worked example.

Package doc comments that justify a package's *location* are the other thing to
sweep for: a relocation can turn one into a self-contradiction. Round 2 had to
rewrite `internal/executor/hashsize`'s ("the planner imports the executor
nowhere") and re-title `internal/storage/file`'s after the `pgtemp` → `file`
package rename.
