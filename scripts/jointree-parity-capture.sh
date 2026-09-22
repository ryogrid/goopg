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
#   FIRESET_QUERIES  optional comma-separated query ids to execute after the
#              EXPLAIN capture. Requires FIRESET_STATUS_OUT; writes one
#              `Q<n> PASS|TIMEOUT|ERROR` record per id and keeps each raw
#              result beside the captures. On the TPC-DS lanes each id runs as
#              `query<n>.sql` through psql on the private clone and a timeout
#              restarts that clone before the next query. On the TPC-H lane
#              (M0145-0021b) there are no .sql files — the bank lives in
#              `cmd/tpch-runner` — so the ids run through
#              `tpch-acceptance-arm.sh QUERIES=…` on ITS private clone and
#              `tpch-fireset-parse.py` maps the arm's per-query lines onto the
#              same three-status vocabulary.
#   TPCH_FIRESET_PGSHAPED  GOOPG_PGSHAPED_DP for the TPC-H fire-set arm
#              (default 1 — the shipped planner, NOT tpch-acceptance-arm.sh's
#              own 0 default).
#   FIRESET_STATUS_OUT  destination for FIRESET_QUERIES status records. The
#              caller owns truncation so several isolated arms can append.
#   FIRESET_TIMEOUT  per-query execution timeout in seconds (default 600).
#   FIRESET_SKIP_CAPTURE  1 = execute FIRESET_QUERIES on a fresh clone without
#              re-capturing plans. Valid only with FIRESET_QUERIES: the outer
#              fire-set gate has already captured and derived those ids.
#   CLONE_LABEL  optional short private-clone directory tag. This is separate
#              from the human-readable capture label so a long gate label
#              cannot exceed the Unix control-socket path limit.
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
    fireset_requested=0
    if [[ -n "${FIRESET_QUERIES:-}" || -n "${FIRESET_STATUS_OUT:-}" ]]; then
        [[ -n "${FIRESET_QUERIES:-}" && -n "${FIRESET_STATUS_OUT:-}" ]] || {
            echo "FATAL: FIRESET_QUERIES and FIRESET_STATUS_OUT must be set together" >&2
            exit 2
        }
        fireset_requested=1
    fi
    if [[ "${FIRESET_SKIP_CAPTURE:-0}" == "1" && "${fireset_requested}" != 1 ]]; then
        echo "FATAL: FIRESET_SKIP_CAPTURE requires FIRESET_QUERIES" >&2
        exit 2
    fi
  if [[ "${FIRESET_SKIP_CAPTURE:-0}" != "1" ]]; then
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
  fi

    # M0145-0021b: the TPC-H fire set is EXECUTED through the acceptance arm,
    # not through psql. TPC-H has no query .sql files in the tree — its bank
    # lives in `cmd/tpch-runner` — and the arm already owns the private clone,
    # the capped server and the per-query budget this needs.
    if [[ "${fireset_requested}" == 1 ]]; then
        mkdir -p "$(dirname "${FIRESET_STATUS_OUT}")" || exit 2
        arm_out="${OUTDIR}/${LABEL}-fireset-arm.txt"
        # PGSHAPED defaults to 1 here, NOT to the arm script's own 0: that
        # default is not the planner configuration goopg ships, and measuring
        # a non-shipped planner is how TPC-H Q9 produced a false red gate for
        # three loops (m0145-0020a-grouped-output-cardinality.md).
        #
        # GATE_STAMP_DIR is redirected so this subset run cannot overwrite the
        # real tmp/gate-stamps/tpch-acceptance-arm.json — a QUERIES subset
        # always stamps NO-COMPARE, and a fire-set execution must never
        # destroy (or fabricate) a gate verdict.
        PGSHAPED="${TPCH_FIRESET_PGSHAPED:-1}" \
        QUERIES="${FIRESET_QUERIES}" \
        PER_Q="${FIRESET_TIMEOUT:-600}" \
        GATE_STAMP_DIR="${OUTDIR}/gate-stamps" \
            "${SCRIPT_DIR}/tpch-acceptance-arm.sh" "${LABEL}-fireset" "${arm_out}" || {
            echo "FATAL: tpch fire-set arm failed (see ${arm_out})" >&2
            exit 1
        }
        python3 "${SCRIPT_DIR}/tpch-fireset-parse.py" "${arm_out}" "${FIRESET_QUERIES}" \
            >>"${FIRESET_STATUS_OUT}" || exit 2
        while read -r qid status; do
            echo "# fireset ${LABEL} ${qid} ${status} (arm ${arm_out})"
        done < <(python3 "${SCRIPT_DIR}/tpch-fireset-parse.py" "${arm_out}" "${FIRESET_QUERIES}")
    fi

    [[ "${FIRESET_SKIP_CAPTURE:-0}" == "1" ]] && exit 0
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

CLONE_LABEL="${CLONE_LABEL:-${LABEL}}"
CLONE="${REPO_ROOT}/tmp/${CLONE_LABEL}-data-${CORPUS}"
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
server_pid=""

stop_clone_server() {
    timeout 60 "${GOOPG_BIN}" stop -D "${CLONE}" >>"${SRV_LOG}" 2>&1 || true
    if [[ -n "${server_pid}" ]]; then
        wait "${server_pid}" 2>/dev/null || true
        server_pid=""
    fi
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
}

start_clone_server() {
    local ready=0
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
        "${GOOPG_BIN}" start -D "${CLONE}" --listen "127.0.0.1:${PORT}" \
        "${hba_arg[@]}" >>"${SRV_LOG}" 2>&1 &
    server_pid=$!
    for _ in $(seq 1 120); do
        kill -0 "${server_pid}" 2>/dev/null || break
        pg_isready -h 127.0.0.1 -p "${PORT}" -U postgres -q >/dev/null 2>&1 && { ready=1; break; }
        sleep 1
    done
    [[ "${ready}" -eq 1 ]] || {
        echo "FATAL: clone server not ready (log ${SRV_LOG})" >&2
        tail -20 "${SRV_LOG}" >&2 || true
        return 1
    }
}

cleanup() {
    stop_clone_server
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT TERM

start_clone_server || exit 5

if [[ "${FIRESET_SKIP_CAPTURE:-0}" == "1" ]]; then
    [[ -n "${FIRESET_QUERIES:-}" ]] || {
        echo "FATAL: FIRESET_SKIP_CAPTURE requires FIRESET_QUERIES" >&2
        exit 2
    }
else
    # goopg arm — CAPTURE_ENGINE is REQUIRED on a non-6543x port
    # (capture-stamp.sh refuses an unlabelled private lane).
    CAPTURE_ENGINE=goopg GOOPG_EXPECT_BIN_SHA256="${EXPECT_BIN_SHA}" \
        "${SCRIPT_DIR}/capture-tpcds.sh" "${PORT}" postgres postgres \
        "${GOOPG_PLANS}" "${LABEL} goopg ${CORPUS} JOINTREE=${JOINTREE}" "${CLONE}" || exit $?
fi

# Optional fire-set execution stays inside this already-isolated clone.  A
# client-side timeout does not guarantee that the server stopped executing the
# statement, so restart the clone before the next query rather than letting a
# contaminated heap make a later status look trustworthy.
if [[ -n "${FIRESET_QUERIES:-}" || -n "${FIRESET_STATUS_OUT:-}" ]]; then
    [[ -n "${FIRESET_QUERIES:-}" && -n "${FIRESET_STATUS_OUT:-}" ]] || {
        echo "FATAL: FIRESET_QUERIES and FIRESET_STATUS_OUT must be set together" >&2
        exit 2
    }
    mkdir -p "$(dirname "${FIRESET_STATUS_OUT}")" || exit 2
    IFS=',' read -r -a fireset_queries <<<"${FIRESET_QUERIES}"
    declare -A seen_fireset_query=()
    for query in "${fireset_queries[@]}"; do
        [[ "${query}" =~ ^[0-9]+$ && "${query}" -gt 0 ]] || {
            echo "FATAL: invalid FIRESET_QUERIES id: ${query}" >&2
            exit 2
        }
        [[ -z "${seen_fireset_query[${query}]:-}" ]] || {
            echo "FATAL: duplicate FIRESET_QUERIES id: ${query}" >&2
            exit 2
        }
        seen_fireset_query[${query}]=1
        query_file="${TPCDS_QUERY_DIR}/query${query}.sql"
        [[ -f "${query_file}" ]] || { echo "FATAL: missing ${query_file}" >&2; exit 2; }
        result_file="${OUTDIR}/${LABEL}-q${query}.result.txt"
        if timeout "${FIRESET_TIMEOUT:-600}" psql -X -v ON_ERROR_STOP=1 \
            -h 127.0.0.1 -p "${PORT}" -U postgres -d postgres \
            -f "${query_file}" >"${result_file}" 2>&1; then
            query_rc=0
        else
            query_rc=$?
        fi
        case "${query_rc}" in
            0) status=PASS ;;
            124) status=TIMEOUT ;;
            *) status=ERROR ;;
        esac
        printf 'Q%s %s\n' "${query}" "${status}" >>"${FIRESET_STATUS_OUT}"
        echo "# fireset ${LABEL} Q${query} ${status} (result ${result_file})"
        if [[ "${status}" == TIMEOUT ]]; then
            stop_clone_server
            start_clone_server || exit 5
        fi
    done
fi

if [[ "${FIRESET_SKIP_CAPTURE:-0}" == "1" ]]; then
    exit 0
fi

# PG arm — the shared :65438 reference is read-only for EXPLAIN (R1).
CAPTURE_ENGINE=pg "${SCRIPT_DIR}/capture-tpcds.sh" "${TPCDS_PG_PORT}" "${PG_DB}" ryo \
    "${PG_PLANS}" "${LABEL} PG18.3 ${CORPUS} reference" || exit $?

finish
exit $?
