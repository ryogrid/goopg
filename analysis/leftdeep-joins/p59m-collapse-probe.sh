#!/usr/bin/env bash
#
# p59m-collapse-probe.sh — M0127-P5.9-m: why the collapse-ON DS05 plan A/B came
# back `same=99 changed=0` when the eligibility instrument
# (internal/planner/collapse_corpus_test.go) says Q72 and Q75 pose a DIFFERENT
# search problem under the flag.
#
# Two facts predict that identical observable and they have opposite
# consequences for the flip:
#   (a) the PG-shaped search ran in both regimes and chose the same plan, or
#   (b) the search never ran on those query levels, so the joinlist the flag
#       changed was never consulted.
# The printed plan cannot separate them; the enumeration trace
# (GOOPG_PGSHAPED_DP_TRACE=1, internal/planner/joinsearchtrace.go) can — it
# records every pair `makeJoinRel` was OFFERED, so "no trace at all" IS the
# evidence for (b).
#
# Usage: analysis/leftdeep-joins/p59m-collapse-probe.sh [outdir]
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="${1:-${REPO_ROOT}/analysis/leftdeep-joins}"
source "${REPO_ROOT}/bench/tpcds/env_tpcds.sh"
export PATH="${REPO_ROOT}/postgres/local_install/bin:${PATH}"
export LD_LIBRARY_PATH="${REPO_ROOT}/postgres/local_install/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"

BIN="${REPO_ROOT}/tmp/goopg-bench-bin"
QDIR="${QDIR:-${REPO_ROOT}/bench/tpcds/runtime_goopg/tpcds-data/queries}"
QUERIES="${QUERIES:-72 75}"
TAG="${TAG:-}"

for collapse in 0 1; do
    log="${OUT}/2026-08-06-p59m-probe${TAG}-collapse${collapse}.server.log"
    plans="${OUT}/2026-08-06-p59m-probe${TAG}-collapse${collapse}.plans.txt"
    "${BIN}" stop -D "${SF05_GOOPG_DATA}" >/dev/null 2>&1 || true
    systemctl --user stop "goopg-p59m-probe.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "goopg-p59m-probe.scope" >/dev/null 2>&1 || true

    GOOPG_CG_UNIT="goopg-p59m-probe" \
    GOOPG_PGSHAPED_COLLAPSE="${collapse}" GOOPG_PGSHAPED_DP_TRACE=1 \
    GOMEMLIMIT=12GiB GOGC=off GOOPG_MEM_HIGH=20G GOOPG_MEM_MAX=24G \
        "${REPO_ROOT}/scripts/goopg-test-run.sh" \
        "${BIN}" start -D "${SF05_GOOPG_DATA}" \
        --listen "${TPCDS_HOST}:${SF05_PORT}" >"${log}" 2>&1 &
    srv=$!
    ready=0
    for _ in $(seq 1 120); do
        kill -0 "${srv}" 2>/dev/null || break
        pg_isready -h "${TPCDS_HOST}" -p "${SF05_PORT}" -q >/dev/null 2>&1 && { ready=1; break; }
        sleep 1
    done
    [[ "${ready}" -eq 1 ]] || { echo "FATAL: server not ready (collapse=${collapse})"; tail -20 "${log}"; exit 5; }

    : >"${plans}"
    for q in ${QUERIES}; do
        echo "===== Q${q} (GOOPG_PGSHAPED_COLLAPSE=${collapse}) =====" >>"${plans}"
        psql -h "${TPCDS_HOST}" -p "${SF05_PORT}" -U "${TPCDS_SUPERUSER}" -d postgres \
            -v ON_ERROR_STOP=0 -X -q -c "$(printf 'EXPLAIN %s' "$(sed -e 's/;[[:space:]]*$//' "${QDIR}/query${q}.sql")")" \
            >>"${plans}" 2>&1
    done
    timeout 60 "${BIN}" stop -D "${SF05_GOOPG_DATA}" >>"${log}" 2>&1 || true
    wait "${srv}" 2>/dev/null || true
    systemctl --user stop "goopg-p59m-probe.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "goopg-p59m-probe.scope" >/dev/null 2>&1 || true
    echo "collapse=${collapse}: plans=${plans} trace-lines=$(grep -c 'DPTRACE' "${log}" 2>/dev/null || echo 0)"
done
