#!/usr/bin/env bash
# M0125-0002 commit 5 — TPC-H plan-snapshot capture, one arm per binary.
#
# Same instrument the c3/c4 arms used, scripted this time because commit 5 is
# the first walker in the series that D2 predicts WILL move plans (exprSide
# decides which conjuncts reach the hash path), so the A/B is expected to be
# re-run — possibly several times — while the moved set is characterised.
#
# Each arm gets a FRESH capped server on the SAME cluster (:65433). Server age
# is held constant across arms (CLAUDE.md benchmark-timing hygiene): a server
# that has already answered a heavy query sits at GOMEMLIMIT with GOGC=off and
# would mimic a regression. EXPLAIN only — planning is the thing under test.
#
# Usage:  capture-tpch.sh <label> <goopg-binary>
# Writes: plan_snapshots/<label>.txt (via cmd/plan-snapshot)
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
REPO_ROOT="$PWD"

LABEL="${1:?usage: capture-tpch.sh <label> <goopg-binary>}"
BIN="${2:?usage: capture-tpch.sh <label> <goopg-binary>}"
PGDATA="${REPO_ROOT}/bench/tpch/runtime_goopg/data"
PG_PORT=65433
PG_HOST=127.0.0.1
CG_UNIT="goopg-tpch-c5"
LOG="${REPO_ROOT}/analysis/m0125-0002-c5-plans-20260803/${LABEL}.server.log"

export PATH="${REPO_ROOT}/postgres/local_install/bin:$PATH"

stop_server() {
    "${BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
}
trap stop_server EXIT

stop_server
hba_arg=()
[[ -f "${PGDATA}/pg_hba.conf" ]] && hba_arg=(--hba "${PGDATA}/pg_hba.conf")
# Always through the cgroup cap (CLAUDE.md hard-won rule #3).
GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${BIN}" start -D "${PGDATA}" \
    --listen "${PG_HOST}:${PG_PORT}" "${hba_arg[@]}" > "${LOG}" 2>&1 &

# 180 s, not 120 s: the c4 loop watched this cluster take ~2.5 min to accept
# after "listener bound" once, replaying WAL.
for _ in $(seq 1 180); do
    pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U tpch >/dev/null 2>&1 && break
    sleep 1
done
pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U tpch >/dev/null 2>&1 || {
    echo "FATAL: goopg (tpch) never became ready — see ${LOG}"; exit 1; }

make -C "${REPO_ROOT}" plan-snapshot-capture LABEL="${LABEL}"
rc=$?
echo "arm ${LABEL} done (rc=${rc}, bin-sha $(sha256sum "${BIN}" | cut -c1-16))"
exit "${rc}"
