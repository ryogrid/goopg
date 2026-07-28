#!/usr/bin/env bash
# Shared environment for every TPC-DS benchmark script.
#
# TPC-DS moved out of bench/tpch/ on 2026-07-27: it used to squat inside the
# TPC-H runtime tree (bench/tpch/runtime_goopg/), and the SF=1 load had
# overwritten the canonical goopg TPC-H cluster. This file is now the single
# source of truth for TPC-DS directories and ports; bench/tpch/env_goopg.sh
# is TPC-H-only again and no TPC-DS script may source it.
#
# Port map (see CLAUDE.md "Benchmark clusters" for the full picture):
#   65432  TPC-H  PostgreSQL reference   bench/tpch/runtime/pgdata
#   65433  TPC-H  goopg bench            bench/tpch/runtime_goopg/data
#   65434  nightly TPC-H clone lane      (ci/batch — reserved, do not use)
#   65435  nightly TPC-DS clone lane     (ci/batch — reserved, do not use)
#   65436  TPC-DS goopg SF=1             bench/tpcds/runtime_goopg/data
#   65437  TPC-DS goopg SF=0.5           bench/tpcds/runtime_goopg/data-sf05
#   65438  TPC-DS PostgreSQL reference   bench/tpcds/runtime/pgdata (dbs tpcds, tpcds05)
#
# (The SF0.5 gate previously defaulted to 65434, silently colliding with the
# nightly TPC-H lane; the move to 65437 fixed that.)
#
# Sourced by scripts/tpcds-*.sh and bench/tpcds/server.sh.

TPCDS_BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${TPCDS_BENCH_DIR}/../.." && pwd)"

# PostgreSQL 18.3 client/server binaries (psql, pg_ctl, pg_isready, …).
PG_PREFIX="${REPO_ROOT}/postgres/local_install"
export PATH="${PG_PREFIX}/bin:${PATH}"
export LD_LIBRARY_PATH="${PG_PREFIX}/lib:${LD_LIBRARY_PATH:-}"

# goopg bench binary (shared with the TPC-H bench; rebuilt by server.sh start).
GOOPG_BIN="${REPO_ROOT}/tmp/goopg-bench-bin"

# --- Directories -----------------------------------------------------------
TPCDS_RUNTIME_DIR="${TPCDS_BENCH_DIR}/runtime_goopg"
TPCDS_PGDATA="${TPCDS_RUNTIME_DIR}/data"                # goopg SF=1 cluster
SF05_GOOPG_DATA="${TPCDS_RUNTIME_DIR}/data-sf05"        # goopg SF=0.5 cluster
TPCDS_PG_DATA="${TPCDS_BENCH_DIR}/runtime/pgdata"       # PostgreSQL reference cluster
TPCDS_DATA_DIR="${TPCDS_RUNTIME_DIR}/tpcds-data"        # SF=1 TSVs + queries/
SF05_DATA_DIR="${TPCDS_RUNTIME_DIR}/tpcds-data-sf05"    # sampled SF=0.5 TSVs
# Results dirs are env-overridable so a one-off probe (e.g. the M0124-0004 solo
# Q35 run) can write to a scratch dir instead of dropping artefacts next to a
# published sweep. Defaults are unchanged, so every existing caller is a no-op.
TPCDS_RESULTS_DIR="${TPCDS_RESULTS_DIR:-${TPCDS_RUNTIME_DIR}/tpcds-results}"
SF05_RESULTS_DIR="${SF05_RESULTS_DIR:-${TPCDS_RUNTIME_DIR}/tpcds-results-sf05}"
TPCDS_QUERY_DIR="${TPCDS_DATA_DIR}/queries"
TPCDS_TOOLS="${REPO_ROOT}/third-party/tpcds-postgres/DSGen-software-code-3.2.0rc1/tools"

# --- Endpoints -------------------------------------------------------------
TPCDS_HOST="127.0.0.1"
TPCDS_PORT="${TPCDS_PORT:-65436}"        # goopg SF=1
SF05_PORT="${SF05_PORT:-65437}"          # goopg SF=0.5
TPCDS_PG_PORT="${TPCDS_PG_PORT:-65438}"  # PostgreSQL reference
TPCDS_SUPERUSER="postgres"               # goopg (auth is trust on loopback)
TPCDS_PG_USER="ryo"                      # PostgreSQL reference cluster owner
TPCDS_PG_DB="${TPCDS_PG_DB:-tpcds}"      # SF=1 database on the PG cluster
SF05_PG_DB="${SF05_PG_DB:-tpcds05}"      # SF=0.5 database on the PG cluster

# --- Contamination guards --------------------------------------------------
# bench_foreign_procs — the argv of every running process EXCEPT this shell and
# its ancestors.
#
# Any `ps | grep <script-name>` guard self-matches: the invoking shell's own
# command line contains the script name, so the guard reports "already running"
# against itself. This is the same trap as `pkill -f goopg` (CLAUDE.md rule 3),
# and it fired for real on 2026-07-29 — the SF0.5 harness refused to start
# because the wrapper `bash -c '… scripts/tpcds-bench-compare.sh …'` string was
# in the process table. Filtering the ancestor chain is what makes these guards
# trustworthy enough to leave switched on.
bench_foreign_procs() {
    local pids=() p=$$
    while [[ -n "$p" && "$p" != "0" && "$p" != "1" ]]; do
        pids+=("$p")
        p=$(ps -o ppid= -p "$p" 2>/dev/null | tr -d ' ')
    done
    local skip; skip=$(IFS='|'; echo "${pids[*]}")
    ps -eo pid,args --no-headers | awk -v skip="^(${skip})$" '$1 !~ skip { $1=""; print }'
}

TPCDS_GOOPG_LOG="${TPCDS_RUNTIME_DIR}/goopg.tpcds.log"
SF05_LOG="${TPCDS_RUNTIME_DIR}/goopg.sf05.log"
TPCDS_PG_LOG="${TPCDS_RUNTIME_DIR}/pg.log"

# --- goopg runtime knobs (same rationale as bench/tpch/env_goopg.sh) -------
export GOMEMLIMIT="${GOMEMLIMIT:-12GiB}"
export GOGC="${GOGC:-off}"

# --- Legacy aliases --------------------------------------------------------
# The tpcds scripts were written against bench/tpch/env_goopg.sh's variable
# names. Exporting the same names here lets each script migrate by swapping
# ONE source line; new code should prefer the TPCDS_* names above.
RUNTIME_DIR="${TPCDS_RUNTIME_DIR}"
PGDATA="${TPCDS_PGDATA}"
PG_HOST="${TPCDS_HOST}"
export PG_PORT="${TPCDS_PORT}"
export PG_SUPERUSER="${TPCDS_SUPERUSER}"
PG_LOG="${TPCDS_GOOPG_LOG}"
