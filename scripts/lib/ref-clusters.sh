#!/usr/bin/env bash
# ref-clusters.sh — definitions + START-ONLY helpers for the shared reference
# clusters (CLAUDE.md "Benchmark clusters"). Sourced by
# scripts/ref-clusters-ensure.sh and scripts/tpch-ref-recover.sh.
#
# Why a START-ONLY path instead of the lifecycle scripts:
#   * bench/tpch/setup_goopg.sh / setup_pg.sh run `goopg init` / `initdb` when
#     PG_VERSION is missing (and wipe the dir under --reset). setup_goopg.sh also
#     rebuilds the SHARED tmp/goopg-bench-bin (which is what turned the live
#     :65433 exe into "(deleted)") and starts the server UNCAPPED via nohup.
#   * bench/tpcds/server.sh `start sf025` also rebuilds its binary
#     (now tmp/goopg-tpcds-bin) and stops the target first.
#
# :65433 serves from the PINNED bench/tpch/runtime_goopg/goopg-bin (gitignored),
# which only scripts/tpch-ref-recover.sh replaces (build to a temp path, then an
# atomic mv, only while :65433 is stopped). No lane builds into it.
# So these helpers issue exactly the start command those scripts issue — same
# data dir, --listen, --hba, log, GOMEMLIMIT/GOGC/GOOPG_ANALYZE_SEED env — but
# never build, never init, never stop, never touch postmaster.pid, and always go
# through scripts/goopg-test-run.sh (the cgroup cap).
#
# Every start closes fd 9 for the child (`9>&-`): callers hold their flock on
# fd 9, and a server that inherited it would pin the lock for its whole life.

_ref_lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REF_REPO_ROOT="$(cd "${_ref_lib_dir}/../.." && pwd)"
REF_PG_BIN_DIR="${REF_REPO_ROOT}/postgres/local_install/bin"
REF_PG_LIB_DIR="${REF_REPO_ROOT}/postgres/local_install/lib"
# 65437 is gate-owned (started/stopped by scripts/tpcds-sf025-regression.sh) and is
# not restarted by default; name it with --only 65437 to start it explicitly.
REF_ALL_PORTS=(65432 65433 65438)
REF_READY_TIMEOUT="${REF_READY_TIMEOUT:-300}"

# ref_cluster_info <port> — sets RC_NAME RC_ENGINE RC_DATA RC_LOG RC_SCOPE RC_BIN.
ref_cluster_info() {
    RC_NAME=""; RC_ENGINE=""; RC_DATA=""; RC_LOG=""; RC_SCOPE=""; RC_BIN=""
    case "$1" in
    65432)
        RC_NAME="pg-tpch"; RC_ENGINE="pg"
        RC_DATA="${REF_REPO_ROOT}/bench/tpch/runtime/pgdata"
        RC_LOG="${REF_REPO_ROOT}/bench/tpch/runtime/postgres.log"   # bench/tpch/env.sh PG_LOG
        RC_BIN="${REF_PG_BIN_DIR}/pg_ctl" ;;
    65433)
        RC_NAME="goopg-tpch"; RC_ENGINE="goopg"
        RC_DATA="${REF_REPO_ROOT}/bench/tpch/runtime_goopg/data"
        RC_LOG="${REF_REPO_ROOT}/bench/tpch/runtime_goopg/goopg.log" # bench/tpch/env_goopg.sh PG_LOG
        RC_SCOPE="goopg-ref-tpch"
        RC_BIN="${REF_GOOPG_TPCH_BIN:-${REF_REPO_ROOT}/bench/tpch/runtime_goopg/goopg-bin}" ;;
    65437)
        RC_NAME="goopg-tpcds-sf025"; RC_ENGINE="goopg"
        RC_DATA="${REF_REPO_ROOT}/bench/tpcds/runtime_goopg/data-sf025"
        RC_LOG="${REF_REPO_ROOT}/bench/tpcds/runtime_goopg/goopg.sf025.log"  # env_tpcds.sh SF025_LOG
        RC_SCOPE="goopg-tpcds-sf025"                                         # same scope as server.sh / the gate
        # The gate's own lane binary first (scripts/tpcds-sf025-regression.sh
        # default since H1(c)), else the one server.sh uses. Never built here.
        if [[ -n "${REF_GOOPG_SF025_BIN:-}" ]]; then
            RC_BIN="${REF_GOOPG_SF025_BIN}"
        elif [[ -x "${REF_REPO_ROOT}/tmp/goopg-sf025-bin" ]]; then
            RC_BIN="${REF_REPO_ROOT}/tmp/goopg-sf025-bin"
        else
            RC_BIN="${REF_REPO_ROOT}/tmp/goopg-tpcds-bin"   # bench/tpcds/env_tpcds.sh default
        fi ;;
    65438)
        RC_NAME="pg-tpcds"; RC_ENGINE="pg"
        RC_DATA="${REF_REPO_ROOT}/bench/tpcds/runtime/pgdata"
        RC_LOG="${REF_REPO_ROOT}/bench/tpcds/runtime_goopg/pg.log"   # env_tpcds.sh TPCDS_PG_LOG
        RC_BIN="${REF_PG_BIN_DIR}/pg_ctl" ;;
    *) return 1 ;;
    esac
    return 0
}

# ref_port_listening <port> — 0 when something LISTENs on 127.0.0.1:<port>.
ref_port_listening() {
    [[ -n "$(ss -Hltn "sport = :$1" 2>/dev/null)" ]]
}

# ref_pidfile_pid <datadir> — first line of postmaster.pid, or empty.
ref_pidfile_pid() {
    local p
    p="$(head -1 "$1/postmaster.pid" 2>/dev/null || true)"
    [[ "${p}" =~ ^[0-9]+$ ]] && echo "${p}"
    return 0
}

# ref_wait_ready <port> <timeout> — poll until the port LISTENs.
ref_wait_ready() {
    local port="$1" t="$2" i
    for ((i = 0; i < t; i++)); do
        ref_port_listening "${port}" && return 0
        sleep 1
    done
    return 1
}

# ref_cluster_start <port> — START ONLY (see header). Assumes ref_cluster_info
# has been checked by the caller (PG_VERSION present, HOLD honoured, not
# listening). Returns 0 once the port listens, 1 otherwise.
ref_cluster_start() {
    local port="$1"
    ref_cluster_info "${port}" || return 1
    if [[ "${RC_ENGINE}" == "pg" ]]; then
        # Same command bench/tpch/setup_pg.sh / bench/tpcds/server.sh issue on
        # their start path; pg_ctl start never initdb's.
        PATH="${REF_PG_BIN_DIR}:${PATH}" \
        LD_LIBRARY_PATH="${REF_PG_LIB_DIR}${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}" \
            "${RC_BIN}" -D "${RC_DATA}" -l "${RC_LOG}" start </dev/null >/dev/null 2>&1 9>&- || return 1
    else
        local hba=()
        [[ -f "${RC_DATA}/pg_hba.conf" ]] && hba=(--hba "${RC_DATA}/pg_hba.conf")
        (
            # Runtime env the lifecycle env files export (bench/tpch/env_goopg.sh,
            # bench/tpcds/env_tpcds.sh) — same GOMEMLIMIT/ANALYZE_SEED defaults.
            # GOGC defaults to 100 here (the env files use `off`): ref lanes are
            # long-lived and mostly idle, so GC must be allowed to shrink the
            # heap between queries instead of pinning it at the all-time peak —
            # the standing ~10 GiB GOGC=off footprint of :65433 was a major
            # contributor to the 2026-09-18 global OOM. Bench lanes keep
            # GOGC=off for GC-CPU-free query timing; override per start via env.
            export GOMEMLIMIT="${GOMEMLIMIT:-12GiB}" GOGC="${GOGC:-100}"
            export GOOPG_ANALYZE_SEED="${GOOPG_ANALYZE_SEED:-20260905}"
            # Keep ref lanes OOM-neutral so that inside the shared
            # goopg-workloads.slice the kernel prefers killing throwaway
            # workloads (goopg-test-run.sh defaults them to +400) first.
            export GOOPG_OOM_SCORE_ADJ="${GOOPG_OOM_SCORE_ADJ:-0}"
            export GOOPG_CG_UNIT="${RC_SCOPE}"
            systemctl --user reset-failed "${RC_SCOPE}.scope" >/dev/null 2>&1 || true
            setsid "${REF_REPO_ROOT}/scripts/goopg-test-run.sh" \
                "${RC_BIN}" start -D "${RC_DATA}" \
                --listen "127.0.0.1:${port}" "${hba[@]}" \
                </dev/null >>"${RC_LOG}" 2>&1 9>&- &
        ) 9>&-
    fi
    ref_wait_ready "${port}" "${REF_READY_TIMEOUT}"
}
