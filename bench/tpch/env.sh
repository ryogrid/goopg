#!/usr/bin/env bash
# Shared environment for the TPC-H benchmark scripts.
# Source this file from every other script. It exports paths,
# connection settings, and benchmark parameters.

set -euo pipefail

# Repo / install layout ------------------------------------------------------
BENCH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${BENCH_DIR}/../.." && pwd)"
PG_PREFIX="${REPO_ROOT}/postgres/local_install"
HAMMERDB_HOME="${REPO_ROOT}/HammerDB-5.0"

# Runtime artefacts (cluster data, logs, sockets) ----------------------------
# All ephemeral state lives under bench/tpch/runtime so it can be wiped
# between runs without touching the repo.
RUNTIME_DIR="${BENCH_DIR}/runtime"
PGDATA="${RUNTIME_DIR}/pgdata"
PGSOCKET_DIR="${RUNTIME_DIR}/sockets"
PG_LOG="${RUNTIME_DIR}/postgres.log"
LOG_DIR="${BENCH_DIR}/logs"

# PostgreSQL connection parameters -------------------------------------------
# Bind to a non-default port to avoid clashing with anything else on
# the box (goopg tests routinely grab ports in the 5543x range).
# HammerDB connects over TCP, so we always listen on 127.0.0.1.
export PG_PORT="${PG_PORT:-65432}"
export PG_HOST="127.0.0.1"

# HammerDB superuser. We create a real Postgres role named "postgres"
# (with this password) so that HammerDB's defaults work unchanged.
export PG_SUPERUSER="postgres"
export PG_SUPERUSER_PASS="postgres"

# TPC-H workload role / database (HammerDB defaults).
export TPCH_USER="tpch"
export TPCH_PASS="tpch"
export TPCH_DB="tpch"

# Smallest scale factor supported by HammerDB's TPC-H workload (~1 GB).
export TPCH_SCALE_FACT="${TPCH_SCALE_FACT:-1}"

# Build threads for the parallel data load. Capped to keep SF=1 builds
# from spawning more virtual users than rows per table.
export TPCH_BUILD_THREADS="${TPCH_BUILD_THREADS:-$(nproc 2>/dev/null || echo 2)}"

# Power-test parameters. One query set, max_parallel_workers_per_gather=2,
# matching HammerDB's stock defaults for a single-VU power test.
export TPCH_TOTAL_QUERYSETS="${TPCH_TOTAL_QUERYSETS:-1}"
export TPCH_DEGREE_OF_PARALLEL="${TPCH_DEGREE_OF_PARALLEL:-2}"

# Tooling --------------------------------------------------------------------
# The locally built psql/initdb/pg_ctl link against the bundled libpq
# (PostgreSQL 18). The system libpq under /usr/lib is older and lacks
# symbols like PQsendPipelineSync, so we must point dlopen at our libs.
export LD_LIBRARY_PATH="${PG_PREFIX}/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"
export PATH="${PG_PREFIX}/bin:${PATH}"

# psql/pg_ctl convenience defaults.
export PGHOST="${PG_HOST}"
export PGPORT="${PG_PORT}"
export PGUSER="${PG_SUPERUSER}"
export PGPASSWORD="${PG_SUPERUSER_PASS}"
export PGDATABASE="postgres"

# HammerDB's TPC-H run.tcl reads $TMP for its result file. Pin it to a
# predictable place so we can read the recorded job id afterwards.
export TMP="${RUNTIME_DIR}/tmp"
mkdir -p "${TMP}" "${LOG_DIR}" "${PGSOCKET_DIR}" "${RUNTIME_DIR}"
