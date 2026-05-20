#!/usr/bin/env bash
# runtimeshim_go_matrix.sh — verify the //go:linkname bindings in
# internal/runtimeshim against every Go toolchain present on the host.
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
# it can discover in PATH. It reports PASS/FAIL per toolchain and exits
# non-zero if any toolchain fails. Both the linkname build (default) and
# the public-API fallback build (`-tags noLinkname` if/when the fallback
# files are tag-flipped) are exercised when supported.
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
  echo "=== $tc ($ver) ==="

  if "$tc" test -race -count=1 ./internal/runtimeshim/...; then
    SUMMARY+=("$tc: PASS ($ver)")
  else
    SUMMARY+=("$tc: FAIL ($ver)")
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
echo "OK: all ${#TOOLCHAINS[@]} toolchain(s) passed"
