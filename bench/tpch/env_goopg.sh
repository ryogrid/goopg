#!/usr/bin/env bash
# Shared environment for the goopg-targeted TPC-H benchmark scripts.
# Mirrors `env.sh` (the upstream-PG variant) but points runtime
# artefacts at a separate goopg-only directory tree so a goopg run
# and an upstream-PG run can coexist on the same machine without
# stepping on each other.
#
# Source from setup_goopg.sh / build_schema_goopg.sh / etc.

set -euo pipefail

BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${BENCH_DIR}/../.." && pwd)"
PG_PREFIX="${REPO_ROOT}/postgres/local_install"
HAMMERDB_HOME="${REPO_ROOT}/HammerDB-5.0"

# Goopg build artefact and runtime tree. Kept under `runtime_goopg/`
# so a parallel upstream-PG run can use `runtime/` undisturbed.
GOOPG_BIN="${REPO_ROOT}/tmp/goopg-bench-bin"
RUNTIME_DIR="${BENCH_DIR}/runtime_goopg"
PGDATA="${RUNTIME_DIR}/data"
PG_LOG="${RUNTIME_DIR}/goopg.log"
LOG_DIR="${BENCH_DIR}/logs"

# HammerDB connects via libpq over TCP. Goopg's auth.DefaultPolicy
# trusts loopback so we don't need scram-sha-256 — a custom pg_hba
# is laid down by setup_goopg.sh that allows trust for the loopback
# addresses HammerDB will use.
export PG_PORT="${PG_PORT:-65433}"
export PG_HOST="127.0.0.1"

# HammerDB hard-codes "postgres" as the bootstrap superuser. Goopg
# doesn't really enforce roles in v0 (auth uses a UserStore that
# accepts anything under trust), so the credentials are
# informational; they show up in HammerDB's startup parameters but
# the server doesn't validate them.
export PG_SUPERUSER="postgres"
export PG_SUPERUSER_PASS="postgres"

export TPCH_USER="tpch"
export TPCH_PASS="tpch"
export TPCH_DB="tpch"

export TPCH_SCALE_FACT="${TPCH_SCALE_FACT:-1}"
# Goopg's parser/executor isn't yet hardened for high concurrency
# under the full HammerDB loader; cap virtual users at 1 by default
# so the build path is the most-tested single-VU shape. Override
# via the env when a future loop wants to stress concurrency.
export TPCH_BUILD_THREADS="${TPCH_BUILD_THREADS:-1}"

export TPCH_TOTAL_QUERYSETS="${TPCH_TOTAL_QUERYSETS:-1}"
export TPCH_DEGREE_OF_PARALLEL="${TPCH_DEGREE_OF_PARALLEL:-1}"

# Use the bundled libpq so HammerDB / psql resolve PG 18 symbols.
export LD_LIBRARY_PATH="${PG_PREFIX}/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"
export PATH="${PG_PREFIX}/bin:${PATH}"

export PGHOST="${PG_HOST}"
export PGPORT="${PG_PORT}"
export PGUSER="${PG_SUPERUSER}"
export PGPASSWORD="${PG_SUPERUSER_PASS}"
export PGDATABASE="postgres"

export TMP="${RUNTIME_DIR}/tmp"
mkdir -p "${TMP}" "${LOG_DIR}" "${RUNTIME_DIR}"

# The shared_buffers arena is a Go heap allocation under GC control.
# GOMEMLIMIT bounds the Go heap footprint. M0061-0004 lowered this
# from 20 GiB to 12 GiB after a WSL2 32 GB host crashed during a
# 22-query sweep with peak VmHWM=16 GB; with shared_buffers=2 GB
# plus a generous safety margin, 12 GiB still fits TPC-H SF=1
# while keeping ≥ 18 GB free for the OS, swap-avoidance, and any
# co-resident processes (browser, IDE, claude-code itself).
# Override with `GOMEMLIMIT=20GiB` for hosts with ≥ 64 GB RAM.
export GOMEMLIMIT=${GOMEMLIMIT:-12GiB}
