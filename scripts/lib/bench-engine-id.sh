#!/usr/bin/env bash
# bench-engine-id.sh — engine provenance helpers (design 0124-0001 rule D4a).
#
# Which engine actually answered a sweep is load-bearing evidence, because every
# M0124/M0125 acceptance is a verdict read off a sweep report. `git log -1`
# cannot carry it: it moves for a docs commit (false alarm) and stays put when
# an uncommitted engine edit enters the sweep at the next rebuild (silent
# mis-provenance — the mechanism behind sweep-20260727-214619's wrong label).
#
# These three helpers live in ONE file, not in each harness, because D4a's
# fields must mean the SAME thing in every report: the SF=1 TPC-DS board, the
# SF0.5 gate, and the TPC-H relation-size arms all print them as
#   # engine-id: <trees> diff=<digest>
#   # engine-binary: running=<sha> on-disk=<sha> (<path>)
# Callers own the *policy* (when to warn, when to declare a sweep void).
#
# Contract: the caller must have REPO_ROOT set; bench_engine_bin_sha with no
# argument reads ${GOOPG_BIN}. Sourced by bench/tpcds/env_tpcds.sh (which every
# TPC-DS harness sources) and scripts/tpch-relsize-arm.sh.

# bench_engine_id — the comparability key: committed engine trees PLUS a digest
# of any uncommitted engine edit. Neither term moves for a docs/tracker commit.
# Deliberately NOT the binary's sha256: `go build` stamps vcs.revision/time/
# modified into the image, so that sha changes on every commit and with dirt
# anywhere in the repo (measured: a docs-only commit moved it e6774c4f ->
# 8f0aac15 and a first-cut guard cried "SWEEP VOID" over unchanged source).
bench_engine_id() {
    ( cd "${REPO_ROOT}" && printf '%s diff=%s' \
        "$(git rev-parse "HEAD:internal" "HEAD:cmd" 2>/dev/null | tr '\n' ' ')" \
        "$(git diff HEAD -- internal cmd 2>/dev/null | sha256sum | cut -c1-12)" )
}

# bench_engine_bin_sha — the image ON DISK at ${1:-$GOOPG_BIN}. Provenance for
# "which build is here now", not a comparability key (see above).
bench_engine_bin_sha() {
    local bin="${1:-${GOOPG_BIN}}"
    [[ -f "${bin}" ]] && sha256sum "${bin}" | cut -c1-16 || echo "absent"
}

# bench_running_engine_sha <datadir> — the image SERVING that cluster. A server
# started before the last rebuild keeps running its now-deleted image, so the
# on-disk binary is not necessarily the one that answered: live state when this
# was written had the SF=1 server 16 h up on 4140b160 while tmp/goopg-bench-bin
# was already 7a4b4f7b. Hash /proc/<postmaster>/exe instead.
bench_running_engine_sha() {
    local pidfile="${1:?datadir required}/postmaster.pid" pid
    [[ -f "${pidfile}" ]] || { echo "no-pidfile"; return; }
    pid="$(head -1 "${pidfile}")"
    [[ -r "/proc/${pid}/exe" ]] || { echo "unreadable"; return; }
    sha256sum "/proc/${pid}/exe" 2>/dev/null | cut -c1-16 || echo "unreadable"
}
