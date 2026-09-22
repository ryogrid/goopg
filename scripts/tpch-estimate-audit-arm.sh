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
#              (COLLAPSE was GOOPG_PGSHAPED_COLLAPSE; take3 C-06 retired the
#              flag and explicit-JOIN flattening is unconditional, so the
#              knob is gone rather than silently inert.)
#   DP_TRACE   1 = run the server with GOOPG_PGSHAPED_DP_TRACE=1 and hand its
#              log to the audit tool as --enum-trace, which adds the clause-6
#              enumeration-provenance section (M0127-P5.9-l-ii). Only
#              meaningful with PGSHAPED=1: the trace is written by the
#              PG-shaped search, so a PGSHAPED=0 arm produces an empty one.
#   JOINTREE   GOOPG_JOINTREE_PIPELINE for this arm (default 0) — the
#              M0145-0002 dual-pipeline knob (AGENT.md G8). Set EXPLICITLY
#              for the same reason as PGSHAPED: an unset flag stops being a
#              well-defined arm the day the default flips, and the knob IS
#              scheduled to flip at M0145-0008's cutover. Knob-on arms are
#              EXPLAIN-only evidence (PLAN_ONLY=1); value gates always run
#              the default pipeline.
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
#   TPCH_AUDIT_ARM_PORT        this arm's PRIVATE port (M0137-0007, default 5582)
#   TPCH_AUDIT_ARM_CLONE_WAIT  seconds to wait for :65433 to go quiet in the clone's
#              `cp -a` FALLBACK path only; the default online pg_basebackup
#              path never waits (M0137-0007, default 60)
#   TPCH_CLONE_MODE  auto|online|copy — see scripts/lib/tpch-private-clone.sh
#
# Exit codes: 3 refused/blocked (incl. source under HOLD, refused clone dst),
# 4 build failed, 5 server not ready, 6 the served /proc/<pid>/exe is not the
# binary this script built (sha256 mismatch / deleted); otherwise the audit's rc.
#
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source=lib/tpch-private-clone.sh
source "${REPO_ROOT}/scripts/lib/tpch-private-clone.sh"

LABEL="${1:?usage: $0 <label> [estimate-audit args...]}"
shift

PG_PREFIX="${REPO_ROOT}/postgres/local_install"
export LD_LIBRARY_PATH="${PG_PREFIX}/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"
export PATH="${PG_PREFIX}/bin:${PATH}"

PG_HOST=127.0.0.1
# M0137-0007: this arm runs on a PRIVATE clone/port, never on the shared
# bench cluster (bench/tpch/runtime_goopg/data, :65433) — see
# scripts/lib/tpch-private-clone.sh.
SRC_DATA="${REPO_ROOT}/bench/tpch/runtime_goopg/data"
SRC_PORT=65433
PG_PORT="${TPCH_AUDIT_ARM_PORT:-5582}"
PGDATA="${REPO_ROOT}/tmp/goopg-audit-arm-tpch-data"
GOOPG_BIN="${GOOPG_BIN:-${REPO_ROOT}/tmp/goopg-acceptance-bin}"
AUDIT_BIN="${AUDIT_BIN:-${REPO_ROOT}/tmp/estimate-audit}"
CG_UNIT="goopg-tpch-audit-${LABEL##*-}"
SRV_LOG="${REPO_ROOT}/tmp/tpch-audit-${LABEL}.server.log"
CLONE_WAIT="${TPCH_AUDIT_ARM_CLONE_WAIT:-60}"

PER_Q="${PER_Q:-600s}"
REFERENCE="${REFERENCE-${REPO_ROOT}/analysis/leftdeep-joins/2026-08-05-p56giii-parity.pg.plans.txt}"

export GOMEMLIMIT="${GOMEMLIMIT:-12GiB}" GOGC="${GOGC:-off}"
export GOOPG_MEM_HIGH="${GOOPG_MEM_HIGH:-20G}" GOOPG_MEM_MAX="${GOOPG_MEM_MAX:-24G}"
export GOOPG_MEM_SWAP_MAX="${GOOPG_MEM_SWAP_MAX:-0}"
export GOOPG_PGSHAPED_DP="${PGSHAPED:-0}"
export GOOPG_PGSHAPED_DP_TRACE="${DP_TRACE:-0}"
export GOOPG_JOINTREE_PIPELINE="${JOINTREE:-0}"
# M0145-0021b: pin the reservoir sample, same default and same reason as
# tpch-acceptance-arm.sh. goopg's statistics are per-connection and ANALYZE is
# sampled, so an UNPINNED arm plans against a different sample every run. This
# lane is the canonical TPC-H plan-parity capture (M0144-0001), and its A/A
# noise was measured on 2026-09-22 before this line existed: two back-to-back
# captures of the SAME binary and SAME arm differed on 20 of 21 queries by
# cost/rows, and Q3 flipped SHAPE outright (GroupAggregate over Gather Merge ->
# HashAggregate over Gather). Unpinned, this lane cannot answer "did my change
# move a plan?" at all, and a fire set derived from its A/B is the whole corpus.
export GOOPG_ANALYZE_SEED="${GOOPG_ANALYZE_SEED:-20260905}"

if pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -q 2>/dev/null; then
    echo "something is already listening on ${PG_HOST}:${PG_PORT} (this arm's private port) — stop it first (${GOOPG_BIN} stop -D ${PGDATA})" >&2
    exit 3
fi
# Evidence hold: never clone a HOLDed source (SKIP-BLOCKED), never write a
# HOLDed / preloss-clone-* destination (see scripts/lib/tpch-private-clone.sh).
tpch_clone_source_held "${SRC_DATA}" && exit 3
tpch_clone_dst_refused "${PGDATA}" && exit 3
[[ -s "${SRC_DATA}/PG_VERSION" ]] || { echo "no loaded TPC-H cluster at ${SRC_DATA}" >&2; exit 3; }
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

# Snapshot-clone the shared cluster into this lane's private data dir
# (M0137-0007) — the only touchpoint with the shared cluster in this script;
# it never stops/starts a server there, and (since the M0139 follow-up) never
# needs it to be down either: a live :65433 is cloned online via
# `pg_basebackup -X fetch`. Stop any stale instance left on the PRIVATE clone
# by a previous crashed run first.
"${GOOPG_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
tpch_private_clone_snapshot "${SRC_DATA}" "${PGDATA}" "${PG_HOST}" "${SRC_PORT}" "${CLONE_WAIT}" \
    || { echo "could not snapshot the shared TPC-H cluster (see above)" >&2; exit 3; }

# Serving-binary verification (M12): pin the sha of the image we are about to
# start; after readiness the served /proc/<pid>/exe must hash to it.
EXPECT_BIN_SHA="$(sha256sum "${GOOPG_BIN}" 2>/dev/null | awk '{print $1}')"
[[ -n "${EXPECT_BIN_SHA}" ]] || { echo "FATAL: cannot hash ${GOOPG_BIN}" >&2; exit 4; }
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

# verify_served_binary — the server answering on PG_PORT must be the image we
# built: postmaster.pid names a live pid listening on PG_PORT, its exe is not
# "(deleted)", and sha256(/proc/<pid>/exe) == EXPECT_BIN_SHA. Fail otherwise.
verify_served_binary() {
    local pidf="${PGDATA}/postmaster.pid" pid exe sha
    pid="$(head -1 "${pidf}" 2>/dev/null || true)"
    if [[ ! "${pid}" =~ ^[0-9]+$ ]] || ! kill -0 "${pid}" 2>/dev/null; then
        echo "FATAL: no live postmaster under ${PGDATA} (pid='${pid}')" >&2; return 1
    fi
    if ! grep -qE "(^|:)${PG_PORT}\$" "${pidf}" 2>/dev/null; then
        echo "FATAL: ${pidf} does not name port ${PG_PORT}" >&2; return 1
    fi
    exe="$(readlink "/proc/${pid}/exe" 2>/dev/null || true)"
    if [[ -z "${exe}" || "${exe}" == *" (deleted)" ]]; then
        echo "FATAL: serving exe of pid ${pid} is '${exe:-unreadable}'" >&2; return 1
    fi
    sha="$(sha256sum "/proc/${pid}/exe" 2>/dev/null | awk '{print $1}')"
    if [[ "${sha}" != "${EXPECT_BIN_SHA}" ]]; then
        echo "FATAL: served binary sha256 ${sha:-unreadable} != built ${GOOPG_BIN} sha256 ${EXPECT_BIN_SHA} (pid ${pid}, ${exe})" >&2
        return 1
    fi
    echo "# served binary verified: pid ${pid} ${exe} sha256 ${sha}"
}
verify_served_binary || exit 6

# Pin serial mode explicitly: this arm's contract (header comment ~line 40)
# is an EXECUTED serial run with max_parallel_workers_per_gather=0, and
# `estimate-audit`'s -serial default flipped to false in M0144-0001.
# A caller may still override via trailing args.
audit_args=(-host "${PG_HOST}" -port "${PG_PORT}" --label "${LABEL}" --timeout "${PER_Q}" -serial=true)
[[ -n "${REFERENCE}" ]] && audit_args+=(--reference "${REFERENCE}")
# The server log IS the trace channel, and the tool reads it after the last
# query has been planned, so no extra synchronisation is needed here.
[[ "${GOOPG_PGSHAPED_DP_TRACE}" == "1" ]] && audit_args+=(--enum-trace "${SRV_LOG}")
[[ "${PLAN_ONLY:-0}" == "1" ]] && audit_args+=(--plan-only)

echo "# audit ${LABEL} GOOPG_PGSHAPED_DP=${GOOPG_PGSHAPED_DP} DP_TRACE=${GOOPG_PGSHAPED_DP_TRACE} PLAN_ONLY=${PLAN_ONLY:-0} started $(date -Is)"
( cd "${REPO_ROOT}" && "${AUDIT_BIN}" "${audit_args[@]}" "$@" )
rc=$?
echo "# audit ${LABEL} finished $(date -Is) rc=${rc}"
exit "${rc}"
