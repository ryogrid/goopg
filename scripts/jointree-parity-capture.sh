#!/usr/bin/env bash
# scripts/jointree-parity-capture.sh — M0145-0002 dual-pipeline harness:
# the knob-arm plan-parity capture recipe (AGENT.md §"Plan-parity harness" G8).
#
# Produces pg-plan-parity-diff.py-compatible `=== Qn` plan files for ONE
# corpus on a private lane, with GOOPG_JOINTREE_PIPELINE exported into the
# measured server's environment. EXPLAIN-only throughout: a plan the
# executor cannot yet run is still comparable evidence, and nothing here
# executes a query body (G8's own rule for the transition).
#
# Usage:
#   scripts/jointree-parity-capture.sh tpch        <label> <outdir>
#   scripts/jointree-parity-capture.sh tpcds-sf025 <label> <outdir>
#   scripts/jointree-parity-capture.sh tpcds-sf1   <label> <outdir>
#
# Writes into <outdir>:
#   <label>.plans.txt      goopg arm — knob state = $JOINTREE
#   <label>-pg.plans.txt   PG 18.3 reference arm
#   <label>-diff.txt       pg-plan-parity-diff.py output (PLAN-PARITY +
#                          CATEGORIES / CATEGORIES-EXCL-MATCH)
#   <label>-class.txt      pg-plan-divergence-class.py per-stage report
#
# Environment:
#   JOINTREE   GOOPG_JOINTREE_PIPELINE for the goopg arm (default 1 — this
#              script exists to measure the knob arm; JOINTREE=0 is a
#              control arm through identical machinery). Value gates never
#              run through here: they are the DEFAULT pipeline's property
#              (G8), so a green knob-arm capture discharges nothing.
#   GOOPG_BIN  engine image for the TPC-DS lane (default
#              tmp/goopg-jointree-bin, built from HEAD here). The TPC-H
#              lane builds its own inside tpch-estimate-audit-arm.sh.
#   AUDIT_BIN  estimate-audit image for the TPC-H lane (default
#              tmp/estimate-audit, built by the arm script before use).
#   NO_BUILD   1 = trust the existing images (TPC-DS lane only; the TPC-H
#              arm has its own NO_BUILD).
#   PORT       private-lane port for the TPC-DS lane (default: first free
#              port in 5590-5599). Never a 6543x port.
#
# Arms:
#   tpch       delegates server bring-up to tpch-estimate-audit-arm.sh
#              (JOINTREE=<n> PLAN_ONLY=1 … -serial=false — the canonical
#              parallel-mode TPC-H parity arm, M0144-0001), then captures
#              the PG reference itself with a bare estimate-audit
#              invocation: -warm-stats=false, NEVER -ref-port — that flag
#              would ANALYZE :65432, which R1 forbids (recipe corrected in
#              m0137-0003 §2 / m0144-0001).
#   tpcds-*    offline `cp -a` clone of the corpus datadir — the source
#              server MUST be down (a live copy is corrupt; the script
#              refuses when the source's postmaster.pid exists) — started
#              on a 55xx port under the cgroup cap, then capture-tpcds.sh
#              on both arms. GOOPG_ANALYZE_SEED / GOMEMLIMIT / GOGC and the
#              port/db map come from bench/tpcds/env_tpcds.sh, the same
#              pins the SF0.25 gate sweep uses. Crash recovery on first
#              start of the clone is expected (copied mid-checkpoint WAL).
#
# Exit codes: 2 usage, 3 refused (live source / busy port / HOLD), 4 build
# failed, 5 server not ready, 6 served-binary sha mismatch; otherwise the
# last capture/diff step's rc.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

CORPUS="${1:-}"
LABEL="${2:-}"
OUTDIR="${3:-}"

case "${CORPUS}" in
    tpch|tpcds-sf025|tpcds-sf1) ;;
    *) echo "usage: $0 {tpch|tpcds-sf025|tpcds-sf1} <label> <outdir>" >&2; exit 2 ;;
esac
[[ -n "${LABEL}" && -n "${OUTDIR}" ]] || { echo "usage: $0 ${CORPUS} <label> <outdir>" >&2; exit 2; }
mkdir -p "${OUTDIR}" || exit 2

JOINTREE="${JOINTREE:-1}"
export GOOPG_JOINTREE_PIPELINE="${JOINTREE}"

GOOPG_PLANS="${OUTDIR}/${LABEL}.plans.txt"
PG_PLANS="${OUTDIR}/${LABEL}-pg.plans.txt"
DIFF_OUT="${OUTDIR}/${LABEL}-diff.txt"
CLASS_OUT="${OUTDIR}/${LABEL}-class.txt"

finish() {
    echo "# diff ${LABEL} (goopg ${GOOPG_PLANS} vs pg ${PG_PLANS})"
    python3 "${SCRIPT_DIR}/pg-plan-parity-diff.py" "${GOOPG_PLANS}" "${PG_PLANS}" >"${DIFF_OUT}"
    local rc=$?
    grep -E "^(PLAN-PARITY|CATEGORIES|CATEGORIES-EXCL-MATCH)" "${DIFF_OUT}" || true
    python3 "${SCRIPT_DIR}/pg-plan-divergence-class.py" "${GOOPG_PLANS}" "${PG_PLANS}" >"${CLASS_OUT}" 2>/dev/null || true
    grep -E "^DIVERGENCE-CLASSES:" "${CLASS_OUT}" || true
    return "${rc}"
}

# ---------------------------------------------------------------- tpch ---
if [[ "${CORPUS}" == "tpch" ]]; then
    # REFERENCE= empty: the §4 ratchet's committed baseline is absent from
    # this checkout and a plan-only run cannot consume it anyway — the
    # recipe captures its own PG arm below instead.
    JOINTREE="${JOINTREE}" PLAN_ONLY=1 REFERENCE= \
        "${SCRIPT_DIR}/tpch-estimate-audit-arm.sh" "${LABEL}" \
        -serial=false -out "${OUTDIR}" || exit $?
    AUDIT_BIN="${AUDIT_BIN:-${REPO_ROOT}/tmp/estimate-audit}"
    [[ -x "${AUDIT_BIN}" ]] || { echo "FATAL: ${AUDIT_BIN} missing after arm build" >&2; exit 4; }
    # PG arm: separate invocation, read-only — never -ref-port (would
    # ANALYZE :65432; R1). -warm-stats=false keeps the session a pure
    # EXPLAIN reader.
    "${AUDIT_BIN}" -plan-only -serial=false -warm-stats=false \
        -port 65432 --label "${LABEL}-pg" -out "${OUTDIR}" || exit $?
    finish
    exit $?
fi

# ------------------------------------------------------------- tpcds-* ---
# env_tpcds.sh ASSIGNS GOOPG_BIN (its own default tmp/goopg-tpcds-bin —
# potentially a binary a live shared server is executing, which rebuilding
# would leave "(deleted)"). Capture the caller's value first, then pin a
# lane-private default: this lane's binary is never a shared server's image.
CALLER_GOOPG_BIN="${GOOPG_BIN:-}"
# shellcheck source=../bench/tpcds/env_tpcds.sh
source "${REPO_ROOT}/bench/tpcds/env_tpcds.sh"

case "${CORPUS}" in
    tpcds-sf025) SRC_DATA="${SF025_GOOPG_DATA}"; PG_DB="${SF025_PG_DB}" ;;
    tpcds-sf1)   SRC_DATA="${TPCDS_PGDATA}";    PG_DB="${TPCDS_PG_DB}" ;;
esac

CLONE="${REPO_ROOT}/tmp/${LABEL}-data-${CORPUS}"
SRV_LOG="${REPO_ROOT}/tmp/${LABEL}-${CORPUS}.server.log"
GOOPG_BIN="${CALLER_GOOPG_BIN:-${REPO_ROOT}/tmp/goopg-jointree-bin}"
CG_UNIT="goopg-jointree-${LABEL##*-}"

# A HOLD file next to the source is evidence — never clone it (R1).
if [[ -f "${SRC_DATA}.HOLD" ]]; then
    echo "FATAL: ${SRC_DATA}.HOLD present — evidence datadir, never cloned" >&2; exit 3
fi
# The clone is an offline `cp -a`; a live source makes it corrupt (WAL
# mid-checkpoint + postmaster.pid). Refuse rather than produce one.
if [[ -f "${SRC_DATA}/postmaster.pid" ]]; then
    echo "FATAL: ${SRC_DATA} is live (postmaster.pid present) — re-run when the server is down" >&2; exit 3
fi
[[ -d "${SRC_DATA}" ]] || { echo "FATAL: source datadir missing: ${SRC_DATA}" >&2; exit 3; }

# Port: explicit $PORT or first free in 5590-5599 (never the 6543x block).
PORT="${PORT:-}"
if [[ -z "${PORT}" ]]; then
    for p in 5590 5591 5592 5593 5594 5595 5596 5597 5598 5599; do
        if ! pg_isready -h 127.0.0.1 -p "${p}" -q 2>/dev/null; then PORT="${p}"; break; fi
    done
fi
[[ -n "${PORT}" ]] || { echo "FATAL: no free 55xx port" >&2; exit 3; }
pg_isready -h 127.0.0.1 -p "${PORT}" -q 2>/dev/null \
    && { echo "FATAL: ${PORT} already listening" >&2; exit 3; }

# Clone: a previous run's clone is regenerable; refuse only a live one.
if [[ -d "${CLONE}" ]]; then
    if [[ -f "${CLONE}/postmaster.pid" ]] \
        && kill -0 "$(head -1 "${CLONE}/postmaster.pid" 2>/dev/null)" 2>/dev/null; then
        echo "FATAL: clone ${CLONE} has a LIVE postmaster — stop it first" >&2; exit 3
    fi
    rm -rf "${CLONE}"
fi
cp -a "${SRC_DATA}" "${CLONE}" || exit 3
rm -f "${CLONE}/postmaster.pid"

if [[ "${NO_BUILD:-0}" != "1" ]]; then
    ( cd "${REPO_ROOT}" && go build -o "${GOOPG_BIN}" ./cmd/goopg ) || exit 4
fi
EXPECT_BIN_SHA="$(sha256sum "${GOOPG_BIN}" 2>/dev/null | awk '{print $1}')"
[[ -n "${EXPECT_BIN_SHA}" ]] || { echo "FATAL: cannot hash ${GOOPG_BIN}" >&2; exit 4; }

hba_arg=()
[[ -f "${CLONE}/pg_hba.conf" ]] && hba_arg=(--hba "${CLONE}/pg_hba.conf")
GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${GOOPG_BIN}" start -D "${CLONE}" --listen "127.0.0.1:${PORT}" \
    "${hba_arg[@]}" >"${SRV_LOG}" 2>&1 &
server_pid=$!

cleanup() {
    timeout 60 "${GOOPG_BIN}" stop -D "${CLONE}" >>"${SRV_LOG}" 2>&1 || true
    wait "${server_pid}" 2>/dev/null || true
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT TERM

ready=0
for _ in $(seq 1 120); do
    kill -0 "${server_pid}" 2>/dev/null || break
    pg_isready -h 127.0.0.1 -p "${PORT}" -U postgres -q >/dev/null 2>&1 && { ready=1; break; }
    sleep 1
done
[[ "${ready}" -eq 1 ]] || { echo "FATAL: clone server not ready (log ${SRV_LOG})"; tail -20 "${SRV_LOG}"; exit 5; }

# goopg arm — CAPTURE_ENGINE is REQUIRED on a non-6543x port
# (capture-stamp.sh refuses an unlabelled private lane).
CAPTURE_ENGINE=goopg GOOPG_EXPECT_BIN_SHA256="${EXPECT_BIN_SHA}" \
    "${SCRIPT_DIR}/capture-tpcds.sh" "${PORT}" postgres postgres \
    "${GOOPG_PLANS}" "${LABEL} goopg ${CORPUS} JOINTREE=${JOINTREE}" "${CLONE}" || exit $?

# PG arm — the shared :65438 reference is read-only for EXPLAIN (R1).
CAPTURE_ENGINE=pg "${SCRIPT_DIR}/capture-tpcds.sh" "${TPCDS_PG_PORT}" "${PG_DB}" ryo \
    "${PG_PLANS}" "${LABEL} PG18.3 ${CORPUS} reference" || exit $?

finish
exit $?
