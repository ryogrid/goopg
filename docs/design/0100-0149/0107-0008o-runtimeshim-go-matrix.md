# Phase D5 — `runtimeshim` Per-Go-Minor Maintenance Script (M0107-0008, loop 15)

Status: accepted
Related: [[0107-0008]] (parent), [[0107-0008a]] Nanotime, [[0107-0008b]] PinP,
[[0107-0008c]] SemaAcquire/Release, [[08-runtime-internals]] §8

## 1. Why this exists

`internal/runtimeshim` binds three primitives to runtime / sync internal
symbols via `//go:linkname`:

| primitive   | linkname target              | source (Go 1.24-1.26) |
|-------------|------------------------------|-----------------------|
| `Nanotime`  | `runtime.nanotime`           | `runtime/time_nofake.go` |
| `PinP`      | `runtime.procPin`            | `runtime/proc.go`       |
| `UnpinP`    | `runtime.procUnpin`          | `runtime/proc.go`       |
| `SemaAcquire` / `SemaRelease` | `sync.runtime_Semacquire` / `runtime_Semrelease` | `runtime/sema.go` |

Each `*_linkname.go` file carries a build-tag pair that enumerates the
*tested* Go minors — currently `//go:build go1.24 && !go1.27`. The chapter
([[08-runtime-internals]] §8) prescribes three maintenance events that
require a tag bump or a new file:

1. **New Go minor released.** Run the suite + a pgbench smoke under the
   new toolchain. If green, bump the upper bound (`!go1.27` → `!go1.28`).
2. **Runtime symbol renamed.** Detected by CI failing to link. Add a new
   file gated on the new minor; tighten the old file's upper bound.
3. **Runtime symbol semantics changed** (rare). Caught by the per-Go-minor
   test failure; resolution is the same as the rename case.

The chapter ends with: *"one file per primitive, one test per primitive,
one CI matrix entry per Go minor."* This doc lands the *script* that makes
"one CI matrix entry per Go minor" mechanical, without depending on a CI
provider (goopg has no `.github/workflows/`).

## 2. What landed

### `scripts/runtimeshim_go_matrix.sh`

A bash script that:

- Discovers Go toolchains in `PATH`. The default `go` is always included;
  every `go1.N`-prefixed binary (installed via
  `go install golang.org/dl/go1.N@latest && go1.N download`) is included
  as well. Explicit args override discovery: `scripts/runtimeshim_go_matrix.sh
  go1.24 go` runs only those two.
- Per toolchain, runs `<tc> test -race -count=1 ./internal/runtimeshim/...`.
- Emits a summary table at the end (`PASS` / `FAIL` / `NOT-FOUND` per
  toolchain) and exits non-zero if any failed.

The script is intentionally local-only — no CI provider lock-in. A
maintainer (or a future CI runner) invokes it identically.

### `make runtimeshim-matrix`

Thin Makefile wrapper that invokes the script and is documented in
`make help`. Lives next to `ralph-state-guard` because it has the same
"developer-loop verification gate" shape (Ralph already runs that target;
the matrix target slots in next to it).

### Maintenance recipe

When a new Go minor (say 1.27) is released:

```sh
go install golang.org/dl/go1.27@latest
go1.27 download
make runtimeshim-matrix         # exercises go (current default) AND go1.27
# If green, bump every `internal/runtimeshim/*_linkname.go` file's
# build tag from `go1.24 && !go1.27` to `go1.24 && !go1.28`.
make runtimeshim-matrix         # confirm with the new tag in place.
```

If `go1.27` fails to link in step 2, see [[08-runtime-internals]] §8 case
(2): introduce a `<primitive>_go1XX.go` file gated on the new minor and
tighten the old file's upper bound.

## 3. What does NOT land

- **No CI provider configuration.** goopg has no `.github/workflows/`.
  Adopting a CI provider is an org-wide infra decision outside the
  M0107-0008 scope. The script is the unit of work; the project can
  shell out to it from any future CI runner verbatim.
- **No fallback-build smoke (`-tags noLinkname`) yet.** The chapter
  envisions a `noLinkname` fallback path (case (3) of §8). The current
  `*_fallback.go` files use the inverse of the linkname build tag, not
  a hand-written `noLinkname` tag — flipping that to a hand-written tag
  is a separate, intentional refactor decision (the inverse-tag pattern
  is cleaner today; the `-tags noLinkname` form only becomes useful once
  a Go minor is *known* to need it). When that refactor lands, this
  script extends to a second pass with `-tags noLinkname`.
- **No pgbench smoke per toolchain.** The chapter calls for a pgbench c=10
  SO sanity run on each new Go minor before bumping the tag. That run is
  expensive (~minutes) and lives at the `analysis/perf-optimize/scripts`
  layer, not in `make runtimeshim-matrix`. The maintenance recipe above
  invokes it as a separate, manual step.

## 4. Verification

- `bash scripts/runtimeshim_go_matrix.sh` (no args, discovery mode) on
  the current host (Go 1.25.0 / Linux/amd64) — PASS, 1 toolchain
  exercised, `internal/runtimeshim` suite green under `-race`.
- `make runtimeshim-matrix` — PASS via the Makefile wrapper.
- Script is `chmod +x` and uses `set -uo pipefail`. The exit code is the
  count of failing toolchains (0 == all green), so any future shell-out
  caller can branch on it directly.

## 5. PG-compat

None — this is a maintenance tooling addition. No production code path
touches the script or the make target. Server behaviour, on-disk format,
and wire protocol are all unaffected.

## 6. Status table — M0107-0008 sub-milestone

| item | state |
|---|---|
| Nanotime shim | landed [[0107-0008a]] |
| PinP / UnpinP shim | landed [[0107-0008b]] |
| SemaAcquire / SemaRelease shim | landed [[0107-0008c]] |
| Per-P xid cache caller | abandoned [[0107-0008d]] (snapshot incompatibility) |
| Activity-registry Nanotime caller | landed [[0107-0008e]] |
| Per-P `stats.Counter` primitive | landed [[0107-0008f]] |
| Eight consecutive consumer migrations (btree, MemRing, AIO totals + per-direction, WAL drain bytes, Checkpointer, bufpool victim, AIO inFlight) | landed [[0107-0008g]]..[[0107-0008n]] |
| Bufpool per-slot Sema wait caller | BLOCKED on M0107-0006 (lockfree bufpool) |
| Per-Go-minor matrix script | landed (this doc) |

The remaining open item — the bufpool per-slot Sema wait caller — is
correctly held back: it consumes the [[0107-0008c]] shim, but its target
site (per-slot wait coordination) only exists after M0107-0006's
lock-free bufpool rewrite displaces the partition-mutex hand-offs that
currently wait via `sync.Mutex`. Migrating to runtime semaphores against
the *current* mutex-based hand-off would be churn that the M0107-0006
rewrite would have to revert.

## 7. PG counterparts

PG does not have an equivalent — its build system is Autoconf-driven and
its runtime is C; there is no per-Go-minor symbol-drift risk. The closest
analogue is PG's `configure` script feature-detecting CPU intrinsics
(`USE_LZ4`, `USE_AVX2`, etc.), but those feature-detect at compile time
rather than tracking a moving upstream runtime ABI.
