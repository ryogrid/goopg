#!/usr/bin/env bash
#
# tpch-acceptance-arm.sh — run ONE arm of the M0127 S5 acceptance bar
# (docs/design/leftdeep-joins/09-verification-and-acceptance.md §3) against the
# TPC-H SF1 cluster: fresh capped server, one serial sweep, server age 0 s at
# sweep start.
#
# Promoted into the repo at M0127-P5.9-d. Run 1 of the bar (§3.1) was driven by
# an untracked tmp/ script, so the protocol the write-up describes could not be
# re-executed from a clean checkout — and §3.1 ends by requiring the whole bar
# to be RE-RUN. A bar whose driver is ephemeral is a bar that gets re-derived
# slightly differently every time; the arm is the independent variable, so the
# harness around it has to be fixed.
#
# Usage:
#   scripts/tpch-acceptance-arm.sh off /tmp/arm-off.txt
#   PGSHAPED=1 scripts/tpch-acceptance-arm.sh on /tmp/arm-on.txt
#   QUERIES=17 PGSHAPED=1 scripts/tpch-acceptance-arm.sh q17 /tmp/q17.txt
#
# Then compare the arms on VALUES, not on row counts (M0127-P5.9-d):
#   tmp/tpch-acceptance-runner -diff /tmp/arm-off.txt /tmp/arm-on.txt
#
# Environment:
#   PGSHAPED   GOOPG_PGSHAPED_DP for this arm (default 0). Set EXPLICITLY on
#              both arms — an unset flag means whatever today's default is, and
#              the arm stops being well-defined the day the default flips
#              (the M0125-0031 lesson, transcribed).
#   COLLAPSE   GOOPG_PGSHAPED_COLLAPSE (default 0)
#   QUERIES    comma-separated query numbers (default: all 22)
#   PER_Q      per-query wall-clock budget in seconds (default 600)
#   DIGEST     1 = pass -digest so the arms can be compared on values (default 1)
#   GOOPG_BIN  engine image (default tmp/goopg-acceptance-bin, built here).
#              NEVER tmp/goopg-bench-bin: ci/batch runs servers from that path
#              for hours and a rebuild under it clobbers the nightly mid-run
#              (ledger row goopg_bench_bin_shared_lane).
#   NO_BUILD   1 = trust the existing images (use this to hold ONE binary across
#              both arms, which is what makes them comparable)
#   FORCE      1 = run even if the nightly CI batch holds the host
#   GOOPG_ANALYZE_SEED
#              Pins ANALYZE's reservoir sample for the arm's server (default
#              20260905). goopg's statistics are per-connection and ANALYZE is
#              sampled, so an unpinned arm plans against a different sample
#              than its counterpart: measured A/A noise of 455 estimate lines
#              and 27 plan-shape lines, including whole join-method flips,
#              which is LARGER than the A/B signal most planner changes carry.
#              Set to 0 to restore wall-clock seeding.
#              See docs/design/planner-gate-reproducibility/DESIGN.md.
#
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source=lib/bench-engine-id.sh
source "${REPO_ROOT}/scripts/lib/bench-engine-id.sh"

ARM="${1:?usage: $0 <arm-name> <out-file> [runner-args...]}"
OUT="${2:?usage: $0 <arm-name> <out-file> [runner-args...]}"
shift 2

PG_PREFIX="${REPO_ROOT}/postgres/local_install"
export LD_LIBRARY_PATH="${PG_PREFIX}/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"
export PATH="${PG_PREFIX}/bin:${PATH}"

PG_HOST=127.0.0.1
PG_PORT=65433
PGDATA="${REPO_ROOT}/bench/tpch/runtime_goopg/data"
GOOPG_BIN="${GOOPG_BIN:-${REPO_ROOT}/tmp/goopg-acceptance-bin}"
RUNNER_BIN="${RUNNER_BIN:-${REPO_ROOT}/tmp/tpch-acceptance-runner}"
CG_UNIT="goopg-tpch-acceptance-${ARM}"
SRV_LOG="${REPO_ROOT}/tmp/tpch-acceptance-${ARM}.server.log"

PER_Q="${PER_Q:-600}"
QUERIES="${QUERIES:-}"
DIGEST="${DIGEST:-1}"

# Memory envelope: the bench default (GOMEMLIMIT=12GiB, GOGC=off). GOOPG_MEM_HIGH
# must sit ABOVE GOMEMLIMIT or the scope parks in the kernel throttle band after
# the first big query and every later timing reads as a regression
# (CLAUDE.md "sweep-tail collapse"; memory cgroup_high_below_gomemlimit).
export GOMEMLIMIT="${GOMEMLIMIT:-12GiB}" GOGC="${GOGC:-off}"
# Statistics envelope: pinned by default so the two arms of an A/B plan against
# the SAME sample (see the GOOPG_ANALYZE_SEED note above).
export GOOPG_ANALYZE_SEED="${GOOPG_ANALYZE_SEED:-20260905}"
export GOOPG_MEM_HIGH="${GOOPG_MEM_HIGH:-20G}" GOOPG_MEM_MAX="${GOOPG_MEM_MAX:-24G}"
export GOOPG_MEM_SWAP_MAX="${GOOPG_MEM_SWAP_MAX:-0}"
export GOOPG_PGSHAPED_DP="${PGSHAPED:-0}"
export GOOPG_PGSHAPED_COLLAPSE="${COLLAPSE:-0}"

# --- pre-flight ------------------------------------------------------------
# A foreign server on the port would be measured instead of ours, and then
# killed by our stop ladder. Refuse rather than guess.
if pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -q 2>/dev/null; then
    echo "something is already listening on ${PG_HOST}:${PG_PORT} — stop it first (bench/tpch/stop_goopg.sh)" >&2
    exit 3
fi
[[ -s "${PGDATA}/PG_VERSION" ]] || { echo "no loaded TPC-H cluster at ${PGDATA}" >&2; exit 3; }
# The bracket around the first character keeps this pattern from matching the
# guard's OWN command line — a bare `pgrep -f ci/batch/run-nightly.sh` self-
# matches and refuses on a quiet host (observed at P5.9 run 2). Same class as
# CLAUDE.md's `pkill -f goopg` rule, one level up: it applies to every guard
# that greps for a peer workload, not just to process kills.
if [[ "${FORCE:-0}" != "1" ]] && pgrep -f "[c]i/batch/run-nightly.sh" >/dev/null 2>&1; then
    echo "the nightly CI batch is running — every timing in this arm would be void. Refusing (FORCE=1 overrides; legitimate only for a values-only run)." >&2
    exit 3
fi

mkdir -p "${REPO_ROOT}/tmp"
if [[ "${NO_BUILD:-0}" != "1" ]]; then
    ( cd "${REPO_ROOT}" && go build -o "${GOOPG_BIN}" ./cmd/goopg ) || exit 4
    ( cd "${REPO_ROOT}" && go build -o "${RUNNER_BIN}" ./cmd/tpch-runner ) || exit 4
fi

"${GOOPG_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true

GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${GOOPG_BIN}" start -D "${PGDATA}" --listen "${PG_HOST}:${PG_PORT}" \
    --hba "${PGDATA}/pg_hba.conf" >"${SRV_LOG}" 2>&1 &
server_pid=$!

# Bounded stop ladder, never a bare `wait`: a leaked backend makes the graceful
# stop block forever (it wedged the nightly for 6h45m on 2026-07-29).
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
[[ "${ready}" -eq 1 ]] || { echo "FATAL: arm ${ARM} server not ready"; tail -20 "${SRV_LOG}"; exit 5; }

runner_args=(-host "${PG_HOST}" -port "${PG_PORT}" -db tpch -user tpch -password tpch
             -per-query-timeout "${PER_Q}s")
[[ -n "${QUERIES}" ]] && runner_args+=(-queries "${QUERIES}")
[[ "${DIGEST}" == "1" ]] && runner_args+=(-digest)

{
    echo "# arm=${ARM} GOOPG_PGSHAPED_DP=${GOOPG_PGSHAPED_DP} GOOPG_PGSHAPED_COLLAPSE=${GOOPG_PGSHAPED_COLLAPSE}"
    echo "# started $(date -Is)"
    echo "# engine-id: $(bench_engine_id)"
    echo "# engine-binary: on-disk=$(bench_engine_bin_sha "${GOOPG_BIN}") (${GOOPG_BIN#"${REPO_ROOT}/"})"
    echo "# per-query cap ${PER_Q}s, serial, digest=${DIGEST}, queries=${QUERIES:-all}"
    echo "# host: load$(cut -d' ' -f1-3 /proc/loadavg)"
} >"${OUT}"
"${RUNNER_BIN}" "${runner_args[@]}" "$@" >>"${OUT}" 2>&1
echo "# finished $(date -Is)" >>"${OUT}"
echo "arm ${ARM} written: ${OUT} ($(wc -l <"${OUT}") lines)"
