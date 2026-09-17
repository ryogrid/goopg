#!/usr/bin/env bash
#
# tpch-ref-recover.sh — OWNER-RUN recovery of the goopg TPC-H reference cluster
# (:65433, bench/tpch/runtime_goopg/data). METHODLOGY3 04-actions E3/E6.
#
# Usage:
#   scripts/tpch-ref-recover.sh --i-am-owner [--evidence-dir DIR] [--from-clone DIR] [--dry-run]
#   scripts/tpch-ref-recover.sh --i-am-owner --evidence-only [--evidence-dir DIR] [--dry-run]
#
# --evidence-only runs steps 1-2 only (graceful stop + verified evidence copy),
# then leaves :65433 STOPPED with data.HOLD in place (created if missing) and
# prints the next steps. Nothing is restored, built or started.
#
# Refuses unless --i-am-owner is given, and always when RALPH_LOOP=1.
#
# Steps (each is echoed; --dry-run runs the read-only checks and prints the
# mutating commands without running them):
#   1  graceful stop of :65433 — `goopg stop -mode fast` over the control socket
#      (the mechanism bench/tpch/stop_goopg.sh uses, but with a long -t and
#      WITHOUT its "rm postmaster.pid on failure" fallback, which would both
#      destroy evidence and let step 2 copy a still-running cluster). Never
#      -mode immediate. Waits for the pid to exit and the port to close.
#   2  preserve evidence: `cp -a` the data dir (plus data.HOLD and goopg.log) to
#      the evidence dir (default tmp/evidence-65433-<date>) after a free-space
#      check; verifies file count and byte size match.
#   3  restore: move the current data dir ASIDE (data.pre-restore-<ts>; never
#      deleted) and COPY (`cp -a`, verified by entry count + byte size) the
#      pre-loss clone in (default bench/tpch/runtime_goopg/preloss-clone-20260915;
#      must have PG_VERSION, >=100 MB, no live postmaster). The clone is only
#      ever read — never moved, modified or started; its own <clone>.HOLD does
#      not block reading and is not copied. The copied-in postmaster.pid (if
#      any) is removed from the NEW data dir only.
#   4  rebuild the PINNED :65433 binary bench/tpch/runtime_goopg/goopg-bin at
#      HEAD from a clean detached worktree (no WIP): build to a temp path next
#      to it, then atomically `mv` it into place — only while :65433 is stopped.
#   5  start :65433 through the cgroup cap (scripts/lib/ref-clusters.sh — the
#      same start command setup_goopg.sh issues, minus its rebuild-from-the-live-
#      tree and uncapped nohup).
#   6  scripts/tpch-spotcheck.sh. On anything but PASS, print the HammerDB
#      reload procedure — never run it.
#   7  only after spotcheck PASS: move data.HOLD into the evidence dir, which
#      lets scripts/ref-clusters-ensure.sh manage :65433 again.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source=lib/ref-clusters.sh
source "${SCRIPT_DIR}/lib/ref-clusters.sh"

PORT=65433
TS="$(date +%Y%m%d-%H%M%S)"
OWNER=0
DRY_RUN=0
EVIDENCE_ONLY=0
EVIDENCE_DIR="${REPO_ROOT}/tmp/evidence-65433-$(date +%Y%m%d)"
CLONE_DIR="${REPO_ROOT}/bench/tpch/runtime_goopg/preloss-clone-20260915"
while [[ $# -gt 0 ]]; do
    case "$1" in
    --i-am-owner)   OWNER=1 ;;
    --dry-run)      DRY_RUN=1 ;;
    --evidence-only) EVIDENCE_ONLY=1 ;;
    --evidence-dir) shift; EVIDENCE_DIR="${1:?--evidence-dir needs a value}" ;;
    --from-clone)   shift; CLONE_DIR="${1:?--from-clone needs a value}" ;;
    -h|--help)      sed -n '2,46p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "tpch-ref-recover: unknown argument '$1'" >&2; exit 2 ;;
    esac
    shift
done
EVIDENCE_DIR="$(realpath -m "${EVIDENCE_DIR}")"
CLONE_DIR="$(realpath -m "${CLONE_DIR}")"

if [[ "${RALPH_LOOP:-}" == "1" ]]; then
    echo "tpch-ref-recover: REFUSED — RALPH_LOOP=1. This script is owner-run only." >&2
    exit 2
fi
if [[ "${OWNER}" -ne 1 ]]; then
    echo "tpch-ref-recover: REFUSED — pass --i-am-owner (owner-run only; it stops and replaces the shared :65433 cluster)." >&2
    exit 2
fi

ref_cluster_info "${PORT}"
DATA="${RC_DATA}"
HOLD="${DATA}.HOLD"
ASIDE="${DATA}.pre-restore-${TS}"
BIN="${REPO_ROOT}/bench/tpch/runtime_goopg/goopg-bin"   # pinned :65433 image (== RC_BIN default)
LOG="${RC_LOG}"

say()  { echo "[tpch-ref-recover] $*"; }
die()  { echo "[tpch-ref-recover] ABORT: $*" >&2; exit 1; }
# run <cmd...> — echo, then execute unless --dry-run.
run() {
    echo "  + $*"
    [[ "${DRY_RUN}" -eq 1 ]] && return 0
    "$@"
}
step() { echo; say "=== step $* ==="; }

tree_count() { find "$1" 2>/dev/null | wc -l; }
tree_bytes() { du -sb "$1" 2>/dev/null | awk '{print $1}'; }
free_bytes() { df -B1 --output=avail "$1" 2>/dev/null | tail -1 | tr -dc '0-9'; }

[[ "${DRY_RUN}" -eq 1 ]] && say "DRY-RUN: read-only checks run; mutating commands are printed only"
say "data=${DATA} evidence=${EVIDENCE_DIR} clone=${CLONE_DIR} bin=${BIN}$([[ ${EVIDENCE_ONLY} -eq 1 ]] && echo ' mode=evidence-only')"
[[ -d "${DATA}" ]] || die "data dir ${DATA} does not exist"
if [[ "${EVIDENCE_ONLY}" -ne 1 ]]; then
    case "${CLONE_DIR}" in
        "${DATA}"|"${DATA}"/*) die "clone ${CLONE_DIR} is (inside) the data dir being replaced" ;;
    esac
fi
if pgrep -f '[c]i/batch/run-nightly.sh' >/dev/null 2>&1; then
    die "the nightly CI batch is running (it copies :65433's data dir); retry when it finishes"
fi

# ---------------------------------------------------------------- step 1
step "1: graceful stop of :${PORT}"
pid="$(ref_pidfile_pid "${DATA}")"
if ref_port_listening "${PORT}" || { [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; }; then
    [[ -n "${pid}" ]] || die ":${PORT} is listening but ${DATA}/postmaster.pid names no pid — not the cluster this script owns"
    say "running: pid ${pid} exe $(readlink "/proc/${pid}/exe" 2>/dev/null || echo '?')"
    STOP_BIN="${BIN}"
    [[ -x "${STOP_BIN}" ]] || STOP_BIN="${REPO_ROOT}/bin/goopg"
    [[ -x "${STOP_BIN}" ]] || die "no goopg binary to issue the stop with (${BIN} or bin/goopg)"
    say "stop issued with ${STOP_BIN} (control socket)"
    run "${STOP_BIN}" stop -D "${DATA}" -mode fast -t 900 || die "graceful stop failed — NOT escalating (no immediate/kill); inspect ${LOG}"
    if [[ "${DRY_RUN}" -ne 1 ]]; then
        for _ in $(seq 1 900); do
            kill -0 "${pid}" 2>/dev/null || ref_port_listening "${PORT}" || break
            sleep 1
        done
        kill -0 "${pid}" 2>/dev/null && die "pid ${pid} still alive after stop"
        ref_port_listening "${PORT}" && die ":${PORT} still listening after stop"
        say "stopped"
    fi
else
    say "not running (port closed, no live pid) — nothing to stop"
fi

# ---------------------------------------------------------------- step 2
step "2: preserve evidence -> ${EVIDENCE_DIR}"
[[ -e "${EVIDENCE_DIR}" ]] && die "evidence dir ${EVIDENCE_DIR} already exists — pass a fresh --evidence-dir"
src_bytes="$(tree_bytes "${DATA}")"; src_bytes="${src_bytes:-0}"
src_count="$(tree_count "${DATA}")"
mkdir_parent="$(dirname "${EVIDENCE_DIR}")"
avail="$(free_bytes "${mkdir_parent}")"
need=$(( src_bytes + src_bytes / 10 + 1073741824 ))   # size + 10% + 1 GiB
say "data dir: ${src_count} entries, ${src_bytes} bytes; free at ${mkdir_parent}: ${avail} bytes; need ${need}"
[[ -n "${avail}" && "${avail}" -ge "${need}" ]] || die "not enough free space for the evidence copy"
run mkdir -p "${EVIDENCE_DIR}" || die "mkdir evidence failed"
run cp -a "${DATA}" "${EVIDENCE_DIR}/data" || die "evidence copy failed"
[[ -f "${LOG}" ]] && { run cp -a "${LOG}" "${EVIDENCE_DIR}/" || die "log copy failed"; }
[[ -f "${HOLD}" ]] && { run cp -a "${HOLD}" "${EVIDENCE_DIR}/" || die "HOLD copy failed"; }
if [[ "${DRY_RUN}" -ne 1 ]]; then
    dst_count="$(tree_count "${EVIDENCE_DIR}/data")"
    dst_bytes="$(tree_bytes "${EVIDENCE_DIR}/data")"
    [[ "${dst_count}" == "${src_count}" ]] || die "evidence entry count ${dst_count} != source ${src_count}"
    [[ "${dst_bytes}" == "${src_bytes}" ]] || die "evidence byte size ${dst_bytes} != source ${src_bytes}"
    say "evidence verified: ${dst_count} entries, ${dst_bytes} bytes"
fi

if [[ "${EVIDENCE_ONLY}" -eq 1 ]]; then
    step "evidence-only: leave :${PORT} stopped, keep HOLD"
    if [[ -e "${HOLD}" ]]; then
        say "HOLD kept: ${HOLD}"
    elif [[ "${DRY_RUN}" -eq 1 ]]; then
        echo "  + write ${HOLD}: evidence preserved at ${EVIDENCE_DIR} by tpch-ref-recover --evidence-only <time>; owner must restore"
    else
        printf 'evidence preserved at %s by tpch-ref-recover --evidence-only %s; owner must restore\n' \
            "${EVIDENCE_DIR}" "$(date -Iseconds)" >"${HOLD}" || die "could not write ${HOLD}"
        say "HOLD written: ${HOLD}"
    fi
    say ":${PORT} is left STOPPED (ref-clusters-ensure will not start it while ${HOLD} exists)."
    say "Next steps (owner):"
    echo "    scripts/tpch-ref-recover.sh --i-am-owner --dry-run --evidence-dir <NEW empty dir>   # review the restore plan"
    echo "    scripts/tpch-ref-recover.sh --i-am-owner --evidence-dir <NEW empty dir>             # stop(no-op) + 2nd evidence copy + restore from ${CLONE_DIR} + rebuild ${BIN} + start + spotcheck + release HOLD"
    echo "    # or, if the clone is unusable: the HammerDB reload procedure in bench/tpch/README.md"
    say "done$([[ "${DRY_RUN}" -eq 1 ]] && echo ' (dry run — nothing was changed)')"
    exit 0
fi

# ---------------------------------------------------------------- step 3
step "3: restore from clone ${CLONE_DIR}"
# The clone's own ${CLONE_DIR}.HOLD protects it from WRITERS (clone lanes,
# ensure); it does not block this read-only copy.
[[ -s "${CLONE_DIR}/PG_VERSION" ]] || die "clone ${CLONE_DIR} has no PG_VERSION"
clone_count="$(tree_count "${CLONE_DIR}")"
clone_bytes="$(tree_bytes "${CLONE_DIR}")"; clone_bytes="${clone_bytes:-0}"
(( clone_bytes >= 100 * 1048576 )) || die "clone is only ${clone_bytes} bytes (< 100 MB) — not a loaded TPC-H cluster"
clone_pid="$(ref_pidfile_pid "${CLONE_DIR}")"
if [[ -n "${clone_pid}" ]] && kill -0 "${clone_pid}" 2>/dev/null; then
    die "clone ${CLONE_DIR} has a LIVE postmaster (pid ${clone_pid}) — stop it first"
fi
avail="$(free_bytes "$(dirname "${DATA}")")"
say "clone: ${clone_bytes} bytes; free at $(dirname "${DATA}"): ${avail} bytes"
[[ -n "${avail}" && "${avail}" -ge $(( clone_bytes + 1073741824 )) ]] || die "not enough free space to copy the clone in"
[[ -e "${ASIDE}" ]] && die "${ASIDE} already exists"
run mv "${DATA}" "${ASIDE}" || die "moving the current data dir aside failed"
if ! run cp -a "${CLONE_DIR}" "${DATA}"; then
    die "clone copy failed — the pre-restore data is intact at ${ASIDE}; move it back with: mv ${ASIDE} ${DATA} (after removing any partial ${DATA})"
fi
if [[ "${DRY_RUN}" -ne 1 ]]; then
    new_count="$(tree_count "${DATA}")"; new_bytes="$(tree_bytes "${DATA}")"
    [[ "${new_count}" == "${clone_count}" && "${new_bytes}" == "${clone_bytes}" ]] \
        || die "restored copy ${new_count} entries/${new_bytes} bytes != clone ${clone_count}/${clone_bytes} — pre-restore data intact at ${ASIDE}"
    say "restored copy verified: ${new_count} entries, ${new_bytes} bytes (clone untouched)"
fi
run rm -f "${DATA}/postmaster.pid"
say "current data kept at ${ASIDE} (never deleted by this script)"

# ---------------------------------------------------------------- step 4
step "4: rebuild ${BIN} at HEAD from a clean worktree (temp path + atomic mv)"
if [[ "${DRY_RUN}" -ne 1 ]]; then
    ref_port_listening "${PORT}" && die ":${PORT} is listening — refusing to replace ${BIN} under a running server"
fi
SRC_WT="${REPO_ROOT}/tmp/recover-src-${TS}"
BIN_TMP="${BIN}.new-${TS}"
head_sha="$(git -C "${REPO_ROOT}" rev-parse HEAD)"
say "HEAD ${head_sha}"
run git -C "${REPO_ROOT}" worktree add --detach "${SRC_WT}" HEAD || die "worktree add failed"
build_rc=0
if [[ "${DRY_RUN}" -eq 1 ]]; then
    echo "  + (cd ${SRC_WT} && go build -o ${BIN_TMP} ./cmd/goopg)"
else
    ( cd "${SRC_WT}" && go build -o "${BIN_TMP}" ./cmd/goopg ) || build_rc=$?
fi
run git -C "${REPO_ROOT}" worktree remove --force "${SRC_WT}"
if [[ "${build_rc}" -ne 0 ]]; then
    rm -f "${BIN_TMP}"
    die "go build failed (rc=${build_rc}); ${BIN} unchanged"
fi
if [[ "${DRY_RUN}" -ne 1 ]]; then
    ref_port_listening "${PORT}" && { rm -f "${BIN_TMP}"; die ":${PORT} started listening during the build — ${BIN} NOT replaced"; }
fi
run mv -f "${BIN_TMP}" "${BIN}" || die "atomic mv ${BIN_TMP} -> ${BIN} failed"
[[ "${DRY_RUN}" -eq 1 ]] || say "${BIN} sha256 $(sha256sum "${BIN}" | awk '{print $1}')"

# ---------------------------------------------------------------- step 5
step "5: start :${PORT} (capped, start-only)"
echo "  + GOOPG_CG_UNIT=${RC_SCOPE} scripts/goopg-test-run.sh ${BIN} start -D ${DATA} --listen 127.0.0.1:${PORT} --hba ${DATA}/pg_hba.conf >>${LOG}"
if [[ "${DRY_RUN}" -ne 1 ]]; then
    REF_GOOPG_TPCH_BIN="${BIN}" ref_cluster_start "${PORT}" \
        || die "server did not start listening within ${REF_READY_TIMEOUT}s — see ${LOG}; HOLD left in place"
    say "listening; exe $(readlink "/proc/$(ref_pidfile_pid "${DATA}")/exe" 2>/dev/null || echo '?')"
fi

# ---------------------------------------------------------------- step 6
step "6: scripts/tpch-spotcheck.sh"
spot_rc=0
# The HOLD is still in place (released only in step 7), so let the spotcheck
# clone exactly this restored data dir despite it.
export TPCH_CLONE_ALLOW_HELD_SOURCE="${DATA}"
run "${REPO_ROOT}/scripts/tpch-spotcheck.sh" || spot_rc=$?
if [[ "${DRY_RUN}" -ne 1 && "${spot_rc}" -ne 0 ]]; then
    say "spotcheck did NOT pass (rc=${spot_rc}; 3 = SKIP-BLOCKED). The clone restore is not good enough."
    say "HammerDB reload procedure (NOT run — destructive; bench/tpch/README.md):"
    echo "    bench/tpch/stop_goopg.sh"
    echo "    bench/tpch/setup_goopg.sh --reset       # wipes ${DATA} (evidence is in ${EVIDENCE_DIR}, pre-restore data in ${ASIDE})"
    echo "    bench/tpch/build_schema_goopg.sh        # ~12 min load + indexes"
    echo "    # then re-pin bench/tpch/spotcheck_expected.env and ci/batch/tpch-row-anchors.csv, and re-run scripts/tpch-spotcheck.sh"
    say "HOLD left in place: ${HOLD}"
    exit 1
fi

# ---------------------------------------------------------------- step 7
step "7: release HOLD"
if [[ -f "${HOLD}" ]]; then
    run mv "${HOLD}" "${EVIDENCE_DIR}/data.HOLD.released-${TS}" || die "could not move ${HOLD}"
else
    say "no HOLD file present"
fi
say "done$([[ "${DRY_RUN}" -eq 1 ]] && echo ' (dry run — nothing was changed)')"
exit 0
