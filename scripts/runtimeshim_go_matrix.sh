#!/usr/bin/env bash
# runtimeshim_go_matrix.sh — verify the //go:linkname bindings in
# internal/port/runtimeshim against every Go toolchain present on the host.
#
# The runtimeshim package binds three primitives to runtime/sync internal
# symbols via //go:linkname (per docs/design/perf-optimize/08-runtime-internals.md
# §8). The build-tag pair on each `*_linkname.go` file expressly enumerates
# the *tested* Go minors. When a new Go minor lands, the maintainer must
# (a) verify the linkname targets still link and behave, and (b) bump the
# upper bound on the build tag from `!go1.27` to `!go1.28` (or introduce a
# new file gated on the new minor if the runtime symbol moved).
#
# This script runs the per-primitive test suite under every Go toolchain
# it can discover in PATH. For each toolchain it exercises both build
# variants in sequence:
#
#   (1) default tags — the //go:linkname path, what production runs;
#   (2) `-tags noLinkname` — the public-API fallback path, satisfying
#       chapter §10's "Fallback build" verification gate. The fallback
#       must remain green on every supported Go minor so the escape
#       hatch is one tag-flip away from being usable when a linkname
#       target moves.
#
# The script reports PASS/FAIL per (toolchain, variant) pair and exits
# non-zero if any pair fails.
#
# Discovery: looks for `go` plus every `go1.N` / `go1.N.M` binary in PATH
# (these are installed via `go install golang.org/dl/go1.N@latest && go1.N
# download`). The default `go` is always included.
#
# Usage:
#   scripts/runtimeshim_go_matrix.sh            # run all discovered toolchains
#   scripts/runtimeshim_go_matrix.sh go1.24 go  # run a specific list

set -uo pipefail

REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
cd "$REPO_ROOT"

discover_toolchains() {
  local seen=()
  local bin
  if command -v go >/dev/null 2>&1; then
    seen+=("go")
  fi
  while IFS= read -r bin; do
    [[ -n "$bin" ]] || continue
    seen+=("$(basename "$bin")")
  done < <(compgen -c 'go1.' 2>/dev/null | sort -u)
  printf '%s\n' "${seen[@]}"
}

declare -a TOOLCHAINS
if [[ $# -gt 0 ]]; then
  TOOLCHAINS=("$@")
else
  mapfile -t TOOLCHAINS < <(discover_toolchains)
fi

if [[ ${#TOOLCHAINS[@]} -eq 0 ]]; then
  echo "no Go toolchains discovered in PATH" >&2
  exit 2
fi

declare -i FAILED=0
declare -a SUMMARY=()

for tc in "${TOOLCHAINS[@]}"; do
  if ! command -v "$tc" >/dev/null 2>&1; then
    SUMMARY+=("$tc: NOT-FOUND")
    FAILED+=1
    continue
  fi
  ver="$("$tc" version 2>/dev/null || echo "unknown")"
  echo "=== $tc ($ver) [linkname] ==="

  if "$tc" test -race -count=1 ./internal/port/runtimeshim/...; then
    SUMMARY+=("$tc linkname: PASS ($ver)")
  else
    SUMMARY+=("$tc linkname: FAIL ($ver)")
    FAILED+=1
  fi

  echo "=== $tc ($ver) [fallback -tags noLinkname] ==="
  if "$tc" test -race -count=1 -tags noLinkname ./internal/port/runtimeshim/...; then
    SUMMARY+=("$tc fallback: PASS ($ver)")
  else
    SUMMARY+=("$tc fallback: FAIL ($ver)")
    FAILED+=1
  fi
done

echo
echo "runtimeshim-matrix summary:"
for line in "${SUMMARY[@]}"; do
  echo "  $line"
done

if (( FAILED > 0 )); then
  echo
  echo "FAIL: $FAILED toolchain(s) failed" >&2
  exit 1
fi
echo
echo "OK: all ${#TOOLCHAINS[@]} toolchain(s) × 2 variant(s) passed"
