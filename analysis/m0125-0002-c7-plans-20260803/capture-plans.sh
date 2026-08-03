#!/usr/bin/env bash
# M0125-0002 commit 7 — TPC-DS SF0.5 plan-vs-plan capture, one arm per binary.
#
# WHY THIS EXISTS RATHER THAN A SWEEP
#
# D4 item 4 asks for the SF0.5 correctness gate on any commit that moves plans.
# Commit 2's TPC-H reading came back byte-identical (strict-text 22/22 MATCH
# against plan_snapshots/m0125-0005-relsize-default-stage2), which is evidence
# about TPC-H and about nothing else: the shapes the conversion newly admits
# (IS NULL, CASE, IN-list, IS DISTINCT FROM, EXTRACT) are rare in TPC-H's 22
# queries and ordinary in TPC-DS's 99.
#
# A full 99-query sweep costs ~2 h, most of it waiting out the 13 known
# timeouts, and it answers "did any ANSWER change?" only by re-running every
# query. This instrument answers the prior question — "did any PLAN change?" —
# from EXPLAIN alone, in minutes. If no plan moves, no answer can move, and the
# sweep would be re-measuring an engine it has already measured. If plans DO
# move, the moved set is exactly the QUERIES= subset the sweep should run.
#
# EXPLAIN only, never EXPLAIN ANALYZE: 13 of the 99 do not finish inside the
# 300 s cap, and planning is the only thing this arm is asking about.
#
# Usage:  capture-plans.sh <arm-name> <goopg-binary>
# Writes: <arm-name>/qNN.txt (one EXPLAIN per query) + <arm-name>.meta
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
REPO_ROOT="$PWD"
# shellcheck source=/dev/null
source "${REPO_ROOT}/bench/tpcds/env_tpcds.sh"

ARM="${1:?usage: capture-plans.sh <arm-name> <goopg-binary>}"
BIN="${2:?usage: capture-plans.sh <arm-name> <goopg-binary>}"
OUT="analysis/m0125-0002-c7-plans-20260803/${ARM}"
CG_UNIT="goopg-tpcds-sf05"
# Q36/Q70/Q86 are dsqgen artefacts that fail on upstream PG too — the same skip
# set the SF0.5 gate uses, so the two artefacts cover the same 96 queries.
SKIP=" 36 70 86 "
# Planning is not the thing under test for a query whose planner hangs; a cap
# keeps one pathological query from stalling the arm, and a capped query is
# recorded as PLANTIMEOUT so the two arms can still be compared cell by cell.
PLAN_TIMEOUT="${PLAN_TIMEOUT:-120}"

export PATH="${REPO_ROOT}/postgres/local_install/bin:$PATH"
mkdir -p "${OUT}"

stop_server() {
    "${BIN}" stop -D "${SF05_GOOPG_DATA}" >/dev/null 2>&1 || true
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
}
trap stop_server EXIT

stop_server
hba_arg=()
[[ -f "${SF05_GOOPG_DATA}/pg_hba.conf" ]] && hba_arg=(--hba "${SF05_GOOPG_DATA}/pg_hba.conf")
# Always through the cgroup cap (CLAUDE.md hard-won rule #3): a planner loop on
# an uncapped server has taken this host to 30 GB RSS before.
GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${BIN}" start -D "${SF05_GOOPG_DATA}" \
    --listen "127.0.0.1:${SF05_PORT}" "${hba_arg[@]}" >> "${SF05_LOG}" 2>&1 &
for _ in $(seq 1 180); do
    pg_isready -h 127.0.0.1 -p "${SF05_PORT}" -U "${TPCDS_SUPERUSER}" >/dev/null 2>&1 && break
    sleep 1
done
pg_isready -h 127.0.0.1 -p "${SF05_PORT}" -U "${TPCDS_SUPERUSER}" >/dev/null 2>&1 || {
    echo "FATAL: goopg (sf05) never became ready — see ${SF05_LOG}"; exit 1; }

{
    echo "# arm:      ${ARM}"
    echo "# binary:   ${BIN}"
    echo "# bin-sha:  $(sha256sum "${BIN}" | cut -c1-16)"
    echo "# captured: $(date -Iseconds)"
    echo "# note:     GOOPG_RELSIZE_FALLBACK left UNSET — since M0125-0005 the"
    echo "#           default IS stage 2, so unset is the shipped planner."
} > "analysis/m0125-0002-c7-plans-20260803/${ARM}.meta"

for q in $(seq 1 99); do
    [[ "${SKIP}" == *" ${q} "* ]] && continue
    f="${TPCDS_QUERY_DIR}/query${q}.sql"
    [[ -f "${f}" ]] || { echo "q${q}: NO QUERY FILE" ; continue; }
    { echo "EXPLAIN"; cat "${f}"; } > /tmp/c7_explain_q.sql
    if timeout "${PLAN_TIMEOUT}" psql -h 127.0.0.1 -p "${SF05_PORT}" \
        -U "${TPCDS_SUPERUSER}" -d postgres -X -q -A -t \
        -f /tmp/c7_explain_q.sql > "${OUT}/q${q}.txt" 2>&1; then
        echo "q${q}: ok"
    else
        echo "PLANTIMEOUT-OR-ERROR (rc=$?)" >> "${OUT}/q${q}.txt"
        echo "q${q}: rc!=0"
    fi
done
echo "arm ${ARM} done"
