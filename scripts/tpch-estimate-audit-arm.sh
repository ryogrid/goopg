#!/usr/bin/env bash
#
# tpch-estimate-audit-arm.sh — run the 09 §5 estimate audit / §4 per-joinrel
# parity ratchet for ONE arm (GOOPG_PGSHAPED_DP off or on) against the TPC-H
# SF1 cluster, on a fresh capped server.
#
# Promoted into the repo at M0127-P5.9 run 3, for the reason P5.9-d promoted
# scripts/tpch-acceptance-arm.sh: the §4/§5 instruments (cmd/estimate-audit)
# are in-repo but their server bring-up was not, so the *measurement* could not
# be re-executed from a clean checkout even though the *tool* could. §4's bar is
# a ratchet — a number that must be comparable across commits — and a ratchet
# whose driver is ephemeral drifts by construction.
#
# Usage:
#   scripts/tpch-estimate-audit-arm.sh <label> [estimate-audit args...]
#
#   PGSHAPED=0 scripts/tpch-estimate-audit-arm.sh 2026-08-05-p59run3-audit-off
#   PGSHAPED=1 scripts/tpch-estimate-audit-arm.sh 2026-08-05-p59run3-audit-on
#
# Writes analysis/leftdeep-joins/<label>.txt (+ .plans.txt) via the audit tool's
# own --out default.
#
# Environment:
#   PGSHAPED   GOOPG_PGSHAPED_DP for this arm (default 0). Set EXPLICITLY on
#              both arms — see tpch-acceptance-arm.sh's note on why an unset
#              flag stops being a well-defined arm the day the default flips.
#   COLLAPSE   GOOPG_PGSHAPED_COLLAPSE (default 0)
#   DP_TRACE   1 = run the server with GOOPG_PGSHAPED_DP_TRACE=1 and hand its
#              log to the audit tool as --enum-trace, which adds the clause-6
#              enumeration-provenance section (M0127-P5.9-l-ii). Only
#              meaningful with PGSHAPED=1: the trace is written by the
#              PG-shaped search, so a PGSHAPED=0 arm produces an empty one.
#   REFERENCE  PG 18.3 reference plans file for the §4 parity gate. Default is
#              the committed capture, so the ratchet stays comparable to the
#              baseline §4.1 pinned; pass empty to skip the parity column, or
#              use --ref-port 65432 in the args to capture live instead.
#   PER_Q      per-query EXPLAIN ANALYZE timeout (default 600s). The query is
#              EXECUTED, serially, with max_parallel_workers_per_gather = 0.
#   PLAN_ONLY  1 = pass --plan-only: EXPLAIN without ANALYZE, so the run yields
#              the §4 clause-6 channel (spine diff + enumeration provenance)
#              and NOT the §5 audit or the §4 parity ratchet. This also LIFTS
#              the nightly-batch refusal below, deliberately: that refusal
#              protects a TIMING measurement, and a plan-only run neither
#              produces one (nothing is executed or timed) nor can be spoiled
#              by one. It still competes for CPU, so it is a few EXPLAINs plus
#              the stats warmup, not a power run. Never use it to sneak a §5
#              arm past the refusal — --plan-only cannot produce one.
#   GOOPG_BIN  engine image (default tmp/goopg-acceptance-bin, built here).
#              NEVER tmp/goopg-bench-bin (nightly lane; see the arm script).
#   NO_BUILD   1 = trust the existing images (use this to hold ONE binary
#              across both arms, which is what makes them comparable)
#   FORCE      1 = run even if the nightly CI batch holds the host
#
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

LABEL="${1:?usage: $0 <label> [estimate-audit args...]}"
shift

PG_PREFIX="${REPO_ROOT}/postgres/local_install"
export LD_LIBRARY_PATH="${PG_PREFIX}/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"
export PATH="${PG_PREFIX}/bin:${PATH}"

PG_HOST=127.0.0.1
PG_PORT=65433
PGDATA="${REPO_ROOT}/bench/tpch/runtime_goopg/data"
GOOPG_BIN="${GOOPG_BIN:-${REPO_ROOT}/tmp/goopg-acceptance-bin}"
AUDIT_BIN="${AUDIT_BIN:-${REPO_ROOT}/tmp/estimate-audit}"
CG_UNIT="goopg-tpch-audit-${LABEL##*-}"
SRV_LOG="${REPO_ROOT}/tmp/tpch-audit-${LABEL}.server.log"

PER_Q="${PER_Q:-600s}"
REFERENCE="${REFERENCE-${REPO_ROOT}/analysis/leftdeep-joins/2026-08-05-p56giii-parity.pg.plans.txt}"

export GOMEMLIMIT="${GOMEMLIMIT:-12GiB}" GOGC="${GOGC:-off}"
export GOOPG_MEM_HIGH="${GOOPG_MEM_HIGH:-20G}" GOOPG_MEM_MAX="${GOOPG_MEM_MAX:-24G}"
export GOOPG_MEM_SWAP_MAX="${GOOPG_MEM_SWAP_MAX:-0}"
export GOOPG_PGSHAPED_DP="${PGSHAPED:-0}"
export GOOPG_PGSHAPED_COLLAPSE="${COLLAPSE:-0}"
export GOOPG_PGSHAPED_DP_TRACE="${DP_TRACE:-0}"

if pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -q 2>/dev/null; then
    echo "something is already listening on ${PG_HOST}:${PG_PORT} — stop it first (bench/tpch/stop_goopg.sh)" >&2
    exit 3
fi
[[ -s "${PGDATA}/PG_VERSION" ]] || { echo "no loaded TPC-H cluster at ${PGDATA}" >&2; exit 3; }
# Bracketed first character: a bare pattern self-matches this very shell.
if pgrep -f "[c]i/batch/run-nightly.sh" >/dev/null 2>&1; then
    if [[ "${PLAN_ONLY:-0}" == "1" ]]; then
        echo "# NOTE: the nightly CI batch is running; proceeding because PLAN_ONLY=1" \
             "measures no timing (see the PLAN_ONLY note in this script's header)." >&2
    elif [[ "${FORCE:-0}" != "1" ]]; then
        echo "the nightly CI batch is running — refusing (PLAN_ONLY=1 is exempt; FORCE=1 overrides)." >&2
        exit 3
    fi
fi

mkdir -p "${REPO_ROOT}/tmp"
if [[ "${NO_BUILD:-0}" != "1" ]]; then
    ( cd "${REPO_ROOT}" && go build -o "${GOOPG_BIN}" ./cmd/goopg ) || exit 4
fi
( cd "${REPO_ROOT}" && go build -o "${AUDIT_BIN}" ./cmd/estimate-audit ) || exit 4

"${GOOPG_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true

GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${GOOPG_BIN}" start -D "${PGDATA}" --listen "${PG_HOST}:${PG_PORT}" \
    --hba "${PGDATA}/pg_hba.conf" >"${SRV_LOG}" 2>&1 &
server_pid=$!

cleanup() {
    timeout 60 "${GOOPG_BIN}" stop -D "${PGDATA}" >>"${SRV_LOG}" 2>&1 || true
    wait "${server_pid}" 2>/dev/null || true
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT TERM

ready=0
for _ in $(seq 1 180); do
    kill -0 "${server_pid}" 2>/dev/null || break
    pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U postgres -q >/dev/null 2>&1 && { ready=1; break; }
    sleep 1
done
[[ "${ready}" -eq 1 ]] || { echo "FATAL: audit ${LABEL} server not ready"; tail -20 "${SRV_LOG}"; exit 5; }

audit_args=(-host "${PG_HOST}" -port "${PG_PORT}" --label "${LABEL}" --timeout "${PER_Q}")
[[ -n "${REFERENCE}" ]] && audit_args+=(--reference "${REFERENCE}")
# The server log IS the trace channel, and the tool reads it after the last
# query has been planned, so no extra synchronisation is needed here.
[[ "${GOOPG_PGSHAPED_DP_TRACE}" == "1" ]] && audit_args+=(--enum-trace "${SRV_LOG}")
[[ "${PLAN_ONLY:-0}" == "1" ]] && audit_args+=(--plan-only)

echo "# audit ${LABEL} GOOPG_PGSHAPED_DP=${GOOPG_PGSHAPED_DP} COLLAPSE=${GOOPG_PGSHAPED_COLLAPSE} DP_TRACE=${GOOPG_PGSHAPED_DP_TRACE} PLAN_ONLY=${PLAN_ONLY:-0} started $(date -Is)"
( cd "${REPO_ROOT}" && "${AUDIT_BIN}" "${audit_args[@]}" "$@" )
rc=$?
echo "# audit ${LABEL} finished $(date -Is) rc=${rc}"
exit "${rc}"
