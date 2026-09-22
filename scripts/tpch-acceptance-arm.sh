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
#   PGSHAPED   GOOPG_PGSHAPED_DP for this arm (default 1 — the shipped
#              planner configuration; owner call 2026-09-22 after the
#              M0145-0020a finding that PGSHAPED=0 measured the legacy DP
#              search, not what ships: Q9 >600 s vs 2.8 s). Set EXPLICITLY on
#              both arms — an unset flag means whatever today's default is,
#              and the arm stops being well-defined the day the default flips
#              (the M0125-0031 lesson, transcribed).
#              (COLLAPSE was GOOPG_PGSHAPED_COLLAPSE; take3 C-06 retired the
#              flag and explicit-JOIN flattening is unconditional, so the
#              knob is gone rather than silently inert.)
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
#   TPCH_ACCEPTANCE_ARM_PORT        this arm's PRIVATE port (M0137-0007, default 5583)
#   TPCH_ACCEPTANCE_ARM_CLONE_WAIT  seconds to wait for :65433 to go quiet in the
#                                   clone's `cp -a` FALLBACK path only; the default
#                                   online pg_basebackup path never waits (default 60)
#   TPCH_CLONE_MODE                 auto|online|copy — scripts/lib/tpch-private-clone.sh
#   ACCEPT_BASELINE  path to a BASELINE arm file (a previous full -digest run of
#              this script, e.g. the OFF arm or HEAD~ arm). When set, after the
#              arm is written it is compared on VALUES with
#              `tpch-acceptance-runner -diff ACCEPT_BASELINE OUT`; a non-MATCH
#              fails the arm (exit 1). Without it the arm is written but the
#              stamp is NO-COMPARE, never PASS.
#
# Exit codes: 0 arm written (and, with ACCEPT_BASELINE, value-identical to the
# baseline); 1 runner failure or baseline compare FAILED; 3 refused/blocked
# (foreign server on the private port, no loaded source cluster, source under
# HOLD, nightly running, clone failed); 4 build failed; 5 server not ready;
# other non-zero = failure. Every exit writes
# tmp/gate-stamps/tpch-acceptance-arm.json (scripts/lib/gate-stamp.sh):
#   PASS         only for a FULL run (no QUERIES subset, digest on, no extra
#                -queries arg) whose runner exited 0 AND whose -diff against
#                ACCEPT_BASELINE reported VERDICT: PASS;
#   NO-COMPARE   exit 0 otherwise (subset probe, no baseline, digest off);
#   SKIP-BLOCKED exit 3; FAIL anything else.
#
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source=lib/bench-engine-id.sh
source "${REPO_ROOT}/scripts/lib/bench-engine-id.sh"
# shellcheck source=lib/tpch-private-clone.sh
source "${REPO_ROOT}/scripts/lib/tpch-private-clone.sh"
# shellcheck source=lib/gate-stamp.sh
source "${REPO_ROOT}/scripts/lib/gate-stamp.sh"
ARM_CLEANUP_ARMED=0
ARM_STAMP_BIN=""   # set once the image this arm measures is known
ARM_COMPARED=0     # 1 only after a full-run -diff vs ACCEPT_BASELINE reported PASS
ARM_NOCOMPARE_REASON="arm exited before the baseline compare"
_arm_on_exit() {
    local rc=$? res
    if [[ "${ARM_CLEANUP_ARMED}" == "1" ]]; then cleanup; fi
    res="$(gate_stamp_result_for_rc "${rc}" 3)"
    if [[ "${res}" == "PASS" && "${ARM_COMPARED}" != "1" ]]; then
        res="NO-COMPARE"
        export GATE_STAMP_REASON="${ARM_NOCOMPARE_REASON}"
    fi
    gate_stamp_write tpch-acceptance-arm "${res}" "${ARM_STAMP_BIN}"
    exit "${rc}"
}
trap _arm_on_exit EXIT

ARM="${1:?usage: $0 <arm-name> <out-file> [runner-args...]}"
OUT="${2:?usage: $0 <arm-name> <out-file> [runner-args...]}"
shift 2

PG_PREFIX="${REPO_ROOT}/postgres/local_install"
export LD_LIBRARY_PATH="${PG_PREFIX}/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"
export PATH="${PG_PREFIX}/bin:${PATH}"

PG_HOST=127.0.0.1
# M0137-0007: this arm runs on a PRIVATE clone/port, never on the shared
# bench cluster (bench/tpch/runtime_goopg/data, :65433) — see
# scripts/lib/tpch-private-clone.sh.
SRC_DATA="${REPO_ROOT}/bench/tpch/runtime_goopg/data"
SRC_PORT=65433
PG_PORT="${TPCH_ACCEPTANCE_ARM_PORT:-5583}"
PGDATA="${REPO_ROOT}/tmp/goopg-acceptance-arm-tpch-data"
GOOPG_BIN="${GOOPG_BIN:-${REPO_ROOT}/tmp/goopg-acceptance-bin}"
RUNNER_BIN="${RUNNER_BIN:-${REPO_ROOT}/tmp/tpch-acceptance-runner}"
CG_UNIT="goopg-tpch-acceptance-${ARM}"
SRV_LOG="${REPO_ROOT}/tmp/tpch-acceptance-${ARM}.server.log"
CLONE_WAIT="${TPCH_ACCEPTANCE_ARM_CLONE_WAIT:-60}"

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
export GOOPG_PGSHAPED_DP="${PGSHAPED:-1}"

# --- pre-flight ------------------------------------------------------------
# A foreign server on this arm's PRIVATE port would be measured instead of
# ours, and then killed by our stop ladder. Refuse rather than guess. This no
# longer checks the shared 65433: this arm never binds it (M0137-0007).
if pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -q 2>/dev/null; then
    echo "something is already listening on ${PG_HOST}:${PG_PORT} (this arm's private port) — stop it first (${GOOPG_BIN} stop -D ${PGDATA})" >&2
    exit 3
fi
tpch_clone_source_held "${SRC_DATA}" && exit 3
tpch_clone_dst_refused "${PGDATA}" && exit 3
[[ -s "${SRC_DATA}/PG_VERSION" ]] || { echo "no loaded TPC-H cluster at ${SRC_DATA}" >&2; exit 3; }
if [[ -n "${ACCEPT_BASELINE:-}" && ! -s "${ACCEPT_BASELINE}" ]]; then
    echo "ACCEPT_BASELINE=${ACCEPT_BASELINE} is missing or empty" >&2
    exit 2
fi
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
ARM_STAMP_BIN="${GOOPG_BIN}"

# Stop any stale instance left on the PRIVATE clone by a previous crashed
# run, then snapshot-clone the shared cluster into it (M0137-0007) — the
# only touchpoint with the shared cluster in this script; it never
# stops/starts a server there, and (since the M0139 follow-up) never needs
# it to be down either: a live :65433 is cloned online via
# `pg_basebackup -X fetch`.
"${GOOPG_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
tpch_private_clone_snapshot "${SRC_DATA}" "${PGDATA}" "${PG_HOST}" "${SRC_PORT}" "${CLONE_WAIT}" \
    || { echo "could not snapshot the shared TPC-H cluster (see above)" >&2; exit 3; }

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
ARM_CLEANUP_ARMED=1   # _arm_on_exit (the EXIT trap) runs cleanup
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
    echo "# arm=${ARM} GOOPG_PGSHAPED_DP=${GOOPG_PGSHAPED_DP}"
    echo "# started $(date -Is)"
    echo "# engine-id: $(bench_engine_id)"
    echo "# engine-binary: on-disk=$(bench_engine_bin_sha "${GOOPG_BIN}") (${GOOPG_BIN#"${REPO_ROOT}/"})"
    echo "# per-query cap ${PER_Q}s, serial, digest=${DIGEST}, queries=${QUERIES:-all}"
    echo "# host: load$(cut -d' ' -f1-3 /proc/loadavg)"
} >"${OUT}"
runner_rc=0
"${RUNNER_BIN}" "${runner_args[@]}" "$@" >>"${OUT}" 2>&1 || runner_rc=$?
echo "# finished $(date -Is) runner-rc=${runner_rc}" >>"${OUT}"
echo "arm ${ARM} written: ${OUT} ($(wc -l <"${OUT}") lines, runner rc=${runner_rc})"
if (( runner_rc != 0 )); then
    echo "arm ${ARM}: tpch-runner exited ${runner_rc} — FAIL" >&2
    exit "${runner_rc}"
fi

# --- baseline value compare (the only route to a PASS stamp) ----------------
full_run=1
[[ -n "${QUERIES}" ]] && { full_run=0; ARM_NOCOMPARE_REASON="subset probe (QUERIES=${QUERIES})"; }
for a in "$@"; do
    case "${a}" in -queries|--queries|-queries=*|--queries=*)
        full_run=0; ARM_NOCOMPARE_REASON="subset probe (-queries in runner args)" ;;
    esac
done
[[ "${DIGEST}" == "1" ]] || { full_run=0; ARM_NOCOMPARE_REASON="DIGEST=${DIGEST}: no values to compare"; }
if [[ "${full_run}" == "1" && -z "${ACCEPT_BASELINE:-}" ]]; then
    ARM_NOCOMPARE_REASON="no ACCEPT_BASELINE given"
fi
if [[ "${full_run}" == "1" && -n "${ACCEPT_BASELINE:-}" ]]; then
    diff_out="${OUT}.diff-vs-baseline.txt"
    diff_rc=0
    "${RUNNER_BIN}" -diff "${ACCEPT_BASELINE}" "${OUT}" >"${diff_out}" 2>&1 || diff_rc=$?
    cat "${diff_out}"
    if (( diff_rc == 0 )) && grep -q '^VERDICT: PASS' "${diff_out}"; then
        ARM_COMPARED=1
        echo "arm ${ARM}: values identical to baseline ${ACCEPT_BASELINE} — PASS"
    else
        echo "arm ${ARM}: baseline compare FAILED (rc=${diff_rc}) vs ${ACCEPT_BASELINE}; see ${diff_out}" >&2
        exit 1
    fi
else
    echo "arm ${ARM}: NO-COMPARE — ${ARM_NOCOMPARE_REASON} (stamp will not read PASS)"
fi
exit 0
