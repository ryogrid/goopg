#!/usr/bin/env bash
# M0125-0047 — restart probe for the Q85 cd1/cd2 alias flip.
#
# The four-arm plan capture is too coarse to answer "does the flip still
# reproduce at HEAD?": each arm is one restart, and a coin flip agreeing twice
# is a 50% event. This restarts the server N times and runs ONLY q85's EXPLAIN,
# recording the alias order of the two customer_demographics scans, so N is
# large enough for the answer to mean something.
#
# Usage:  probe-q85-restarts.sh <goopg-binary> <label> [restarts]
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
REPO_ROOT="$PWD"
# shellcheck source=/dev/null
source "${REPO_ROOT}/bench/tpcds/env_tpcds.sh"

BIN="${1:?usage: probe-q85-restarts.sh <binary> <label> [restarts]}"
LABEL="${2:?usage: probe-q85-restarts.sh <binary> <label> [restarts]}"
N="${3:-8}"
OUT="analysis/m0125-0047/probe-${LABEL}.txt"
CG_UNIT="goopg-tpcds-sf05"

export PATH="${REPO_ROOT}/postgres/local_install/bin:$PATH"

stop_server() {
    "${BIN}" stop -D "${SF05_GOOPG_DATA}" >/dev/null 2>&1 || true
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
}
trap stop_server EXIT

: > "${OUT}"
echo "# binary:  ${BIN} ($(sha256sum "${BIN}" | cut -c1-16))" >> "${OUT}"
echo "# label:   ${LABEL}   restarts: ${N}" >> "${OUT}"

{ echo "EXPLAIN"; cat "${TPCDS_QUERY_DIR}/query85.sql"; } > /tmp/m47_q85.sql

for i in $(seq 1 "${N}"); do
    stop_server
    hba_arg=()
    [[ -f "${SF05_GOOPG_DATA}/pg_hba.conf" ]] && hba_arg=(--hba "${SF05_GOOPG_DATA}/pg_hba.conf")
    GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
        "${BIN}" start -D "${SF05_GOOPG_DATA}" \
        --listen "127.0.0.1:${SF05_PORT}" "${hba_arg[@]}" >> "${SF05_LOG}" 2>&1 &
    for _ in $(seq 1 180); do
        pg_isready -h 127.0.0.1 -p "${SF05_PORT}" -U "${TPCDS_SUPERUSER}" >/dev/null 2>&1 && break
        sleep 1
    done
    plan=$(timeout 120 psql -h 127.0.0.1 -p "${SF05_PORT}" -U "${TPCDS_SUPERUSER}" \
        -d postgres -X -q -A -t -f /tmp/m47_q85.sql 2>&1)
    # The alias order of the two customer_demographics scans, top to bottom.
    order=$(printf '%s\n' "${plan}" | grep -oE 'customer_demographics cd[12]' | \
        sed 's/customer_demographics //' | tr '\n' ',')
    # A whole-plan digest catches a reordering anywhere else in the tree too.
    digest=$(printf '%s' "${plan}" | sha256sum | cut -c1-16)
    echo "restart ${i}: cd-order=${order} plan-sha=${digest}" >> "${OUT}"
    echo "restart ${i}: cd-order=${order} plan-sha=${digest}"
done
stop_server
echo "probe ${LABEL} done -> ${OUT}"
