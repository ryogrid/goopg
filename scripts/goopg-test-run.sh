#!/usr/bin/env bash
#
# goopg-test-run.sh — run a command inside a memory-capped cgroup v2 scope.
#
# Purpose: confine goopg (and the psql / pgbench / go-test drivers that
# exercise it) to a per-run memory budget. When the budget is exceeded, the
# cgroup-local OOM killer SIGKILLs only this command's process tree; the
# system-wide OOM killer never fires. On WSL2 the system-wide OOM killer can
# take down the entire VM, so this containment is the difference between "the
# test died" and "the whole distro died".
#
# It also sets MemorySwapMax=0 for the scope by default: on a host with a large
# swap file (this box has 64 GiB) an unbounded goopg first thrashes swap — the
# VM goes unresponsive for minutes — long before the OOM killer fires. Denying
# the scope swap turns that slow freeze into a prompt, clean kill.
#
# Requirements: systemd with the `memory` controller delegated to the user
# manager (true on this repo's WSL2 box; verified via the per-user
# cgroup.controllers file). If delegation is unavailable the command still
# runs, but UNCAPPED, with a loud warning — so the script is safe on CI hosts
# and non-systemd machines.
#
# Usage:
#   scripts/goopg-test-run.sh <command> [args...]
#
# Examples:
#   scripts/goopg-test-run.sh ./bin/goopg start -D tmp/goopg-data --listen 127.0.0.1:5533
#   scripts/goopg-test-run.sh go test -tags integration ./internal/testport/...
#   scripts/goopg-test-run.sh pgbench -i -s 10 -h 127.0.0.1 -p 5533 -U postgres postgres
#
# Tunables (environment variables; defaults sized for a 32 GiB host):
#   GOOPG_MEM_HIGH      soft cap — reclaim + throttle starts here     (default 20G)
#   GOOPG_MEM_MAX       hard cap — cgroup-local OOM kill happens here  (default 24G)
#   GOOPG_MEM_SWAP_MAX  swap allowed to the scope                      (default 0)
#   GOOPG_CG_UNIT       transient scope unit name                      (default goopg-test)
#   GOMEMLIMIT          Go soft heap target; exported if unset         (default 18GiB)
#
# Stopping a backgrounded run by name:
#   systemctl --user stop "<unit>.scope"
#   systemctl --user reset-failed "<unit>.scope"   # only if a failed unit lingers
#
# Concurrency: each concurrent capped command needs a distinct GOOPG_CG_UNIT
# (systemd refuses to start a second scope with a name already in use).
#
set -euo pipefail

if [ "$#" -eq 0 ]; then
    echo "usage: $0 <command> [args...]" >&2
    exit 2
fi

MEM_HIGH="${GOOPG_MEM_HIGH:-20G}"
MEM_MAX="${GOOPG_MEM_MAX:-24G}"
MEM_SWAP_MAX="${GOOPG_MEM_SWAP_MAX:-0}"
CG_UNIT="${GOOPG_CG_UNIT:-goopg-test}"

# Keep the Go GC's target below the cgroup soft cap so goopg tries to stay in
# budget on its own before the kernel starts throttling the scope.
export GOMEMLIMIT="${GOMEMLIMIT:-18GiB}"

uid="$(id -u)"
controllers="/sys/fs/cgroup/user.slice/user-${uid}.slice/user@${uid}.service/cgroup.controllers"

if command -v systemd-run >/dev/null 2>&1 \
    && [ -r "$controllers" ] \
    && grep -qw memory "$controllers"; then
    echo "goopg-test-run: scope=${CG_UNIT} MemoryHigh=${MEM_HIGH} MemoryMax=${MEM_MAX} MemorySwapMax=${MEM_SWAP_MAX} GOMEMLIMIT=${GOMEMLIMIT}" >&2
    # --collect: unload the scope unit even on failure/OOM, so its name is free
    #            for the next run without a manual `reset-failed`.
    # --expand-environment=no: pass argv through verbatim (the caller's shell
    #            already expanded it); also silences a systemd deprecation note.
    exec systemd-run --user --scope --quiet --collect \
        --expand-environment=no \
        --unit="${CG_UNIT}" \
        -p MemoryHigh="${MEM_HIGH}" \
        -p MemoryMax="${MEM_MAX}" \
        -p MemorySwapMax="${MEM_SWAP_MAX}" \
        -- "$@"
fi

echo "goopg-test-run: WARNING — systemd user-cgroup memory delegation unavailable; running UNCAPPED." >&2
echo "goopg-test-run: a runaway goopg may trip the system-wide OOM killer (on WSL2 this can kill the VM)." >&2
exec "$@"
