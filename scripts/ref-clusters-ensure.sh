#!/usr/bin/env bash
#
# ref-clusters-ensure.sh — bring the shared reference clusters back UP when
# they are found stopped. It never stops, restarts, resets, reloads, inits or
# rebuilds anything.
#
# Usage:
#   scripts/ref-clusters-ensure.sh [--dry-run] [--strict] [--only PORT[,PORT...]]
#
# Clusters (CLAUDE.md "Benchmark clusters"):
#   65432  PostgreSQL TPC-H   bench/tpch/runtime/pgdata
#   65433  goopg TPC-H        bench/tpch/runtime_goopg/data
#   65437  goopg TPC-DS SF0.25 bench/tpcds/runtime_goopg/data-sf025
#   65438  PostgreSQL TPC-DS  bench/tpcds/runtime/pgdata
#
# Per cluster:
#   listening on its port              -> OK, nothing done
#   <datadir>.HOLD exists              -> HOLD, refused (reason logged from the file)
#   <datadir>/PG_VERSION missing       -> REFUSE (would need init — never done here)
#   goopg binary missing               -> REFUSE (never built here)
#   postmaster.pid names a LIVE pid    -> REFUSE (process exists but not listening:
#                                         starting up / wedged — not ours to touch)
#   postmaster.pid names a dead pid    -> REFUSE: an unclean shutdown. Writes
#                                         <datadir>.HOLD ("unclean shutdown detected
#                                         by ref-clusters-ensure <time>; owner must
#                                         inspect") so nothing restarts it before the
#                                         owner has looked; pidfile left in place
#   start failed < 30 min ago          -> SKIP (backoff marker
#                                         tmp/ref-clusters-ensure/<port>.fail;
#                                         override dir: REF_ENSURE_FAIL_DIR, window:
#                                         REF_ENSURE_BACKOFF_SEC, default 1800)
#   a peer owns the cluster            -> SKIP (sf025 gate running for :65437,
#                                         tpch-ref-recover.sh running for :65433)
#   otherwise                          -> START via scripts/lib/ref-clusters.sh
#                                         (start-only; capped; fd 9 closed)
#
# Every action is appended to ci/logs/ref-clusters-ensure.log
# (override: REF_ENSURE_LOG). Exit 0 always, unless --strict, where any cluster
# not OK at the end (HOLD/REFUSE/SKIP/start failure) exits 1. A concurrent
# invocation (flock on tmp/ref-clusters-ensure.lock) exits 0 without doing
# anything.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source=lib/ref-clusters.sh
source "${SCRIPT_DIR}/lib/ref-clusters.sh"

DRY_RUN=0
STRICT=0
ONLY=""
while [[ $# -gt 0 ]]; do
    case "$1" in
    --dry-run) DRY_RUN=1 ;;
    --strict)  STRICT=1 ;;
    --only)    shift; ONLY="${1:-}" ;;
    -h|--help) sed -n '2,43p' "${BASH_SOURCE[0]}"; exit 0 ;;
    *) echo "ref-clusters-ensure: unknown argument '$1'" >&2; exit 2 ;;
    esac
    shift
done

LOG="${REF_ENSURE_LOG:-${REPO_ROOT}/ci/logs/ref-clusters-ensure.log}"
LOCK="${REF_ENSURE_LOCK:-${REPO_ROOT}/tmp/ref-clusters-ensure.lock}"
FAIL_DIR="${REF_ENSURE_FAIL_DIR:-${REPO_ROOT}/tmp/ref-clusters-ensure}"
BACKOFF_SEC="${REF_ENSURE_BACKOFF_SEC:-1800}"
mkdir -p "$(dirname "${LOG}")" "$(dirname "${LOCK}")" "${FAIL_DIR}"

log() {
    local line
    line="$(date -Iseconds) [$$]$([[ ${DRY_RUN} -eq 1 ]] && echo ' DRY-RUN') $*"
    echo "${line}"
    echo "${line}" >>"${LOG}" 2>/dev/null || true
}

exec 9>"${LOCK}"
if ! flock -n 9; then
    log "another ref-clusters-ensure holds ${LOCK} — exiting"
    exit 0
fi

# peer_running <pattern> — some running process's argv matches <pattern>.
# The bracketed first character keeps the pattern from matching pgrep itself.
peer_running() {
    pgrep -f "$1" >/dev/null 2>&1
}

ports=("${REF_ALL_PORTS[@]}")
if [[ -n "${ONLY}" ]]; then
    IFS=',' read -ra ports <<<"${ONLY}"
fi

not_ok=0
for port in "${ports[@]}"; do
    if ! ref_cluster_info "${port}"; then
        log ":${port} REFUSE unknown cluster port"
        not_ok=1; continue
    fi
    tag=":${port} ${RC_NAME}"

    if ref_port_listening "${port}"; then
        log "${tag} OK (listening)"
        continue
    fi

    hold="${RC_DATA}.HOLD"
    if [[ -e "${hold}" ]]; then
        log "${tag} HOLD — not started; ${hold}: $(tr '\n' ' ' <"${hold}" 2>/dev/null | cut -c1-400)"
        not_ok=1; continue
    fi
    if [[ ! -s "${RC_DATA}/PG_VERSION" ]]; then
        log "${tag} REFUSE — no PG_VERSION under ${RC_DATA} (would need init/reload; never done here)"
        not_ok=1; continue
    fi
    if [[ ! -x "${RC_BIN}" ]]; then
        log "${tag} REFUSE — binary ${RC_BIN} missing (never built here)"
        not_ok=1; continue
    fi
    case "${port}" in
    65433)
        if peer_running '[s]cripts/tpch-ref-recover\.sh'; then
            log "${tag} SKIP — scripts/tpch-ref-recover.sh is running (owner recovery owns this cluster)"
            not_ok=1; continue
        fi ;;
    65437)
        if peer_running '[t]pcds-sf025-regression\.sh'; then
            log "${tag} SKIP — the sf025 gate is running (it owns this cluster's lifecycle)"
            not_ok=1; continue
        fi ;;
    esac
    pid="$(ref_pidfile_pid "${RC_DATA}")"
    if [[ -n "${pid}" ]]; then
        if kill -0 "${pid}" 2>/dev/null; then
            log "${tag} REFUSE — postmaster.pid names LIVE pid ${pid} but port is not listening (starting up or wedged; not touched)"
            not_ok=1; continue
        fi
        reason="unclean shutdown detected by ref-clusters-ensure $(date -Iseconds); owner must inspect (stale postmaster.pid names dead pid ${pid})"
        if [[ ${DRY_RUN} -eq 1 ]]; then
            log "${tag} REFUSE — stale postmaster.pid (pid ${pid} dead): WOULD WRITE ${hold}: ${reason}"
        elif ( set -o noclobber; printf '%s\n' "${reason}" >"${hold}" ) 2>/dev/null; then
            log "${tag} REFUSE — stale postmaster.pid (pid ${pid} dead); wrote ${hold}: ${reason}"
        else
            log "${tag} REFUSE — stale postmaster.pid (pid ${pid} dead); could not write ${hold} (exists or unwritable)"
        fi
        not_ok=1; continue
    fi
    failf="${FAIL_DIR}/${port}.fail"
    if [[ -e "${failf}" ]]; then
        age=$(( $(date +%s) - $(stat -c %Y "${failf}" 2>/dev/null || echo 0) ))
        if (( age < BACKOFF_SEC )); then
            log "${tag} SKIP — start failed ${age}s ago (backoff ${BACKOFF_SEC}s; ${failf}: $(head -1 "${failf}" 2>/dev/null | cut -c1-200))"
            not_ok=1; continue
        fi
        log "${tag} note: previous start failure marker is ${age}s old (>= ${BACKOFF_SEC}s) — retrying"
    fi

    if [[ ${DRY_RUN} -eq 1 ]]; then
        if [[ "${RC_ENGINE}" == "pg" ]]; then
            log "${tag} WOULD START: ${RC_BIN} -D ${RC_DATA} -l ${RC_LOG} start"
        else
            log "${tag} WOULD START: GOOPG_CG_UNIT=${RC_SCOPE} scripts/goopg-test-run.sh ${RC_BIN} start -D ${RC_DATA} --listen 127.0.0.1:${port}$([[ -f ${RC_DATA}/pg_hba.conf ]] && echo " --hba ${RC_DATA}/pg_hba.conf") >>${RC_LOG}"
        fi
        continue
    fi

    log "${tag} STARTING (${RC_ENGINE}, data ${RC_DATA}, bin ${RC_BIN}, log ${RC_LOG})"
    if ref_cluster_start "${port}"; then
        log "${tag} STARTED (listening)"
        rm -f "${failf}"
    else
        log "${tag} START FAILED — not listening after ${REF_READY_TIMEOUT}s; see ${RC_LOG}; backoff marker ${failf}"
        printf '%s start failed (not listening after %ss); see %s\n' "$(date -Iseconds)" "${REF_READY_TIMEOUT}" "${RC_LOG}" >"${failf}" 2>/dev/null || true
        not_ok=1
    fi
done

if [[ ${STRICT} -eq 1 && ${not_ok} -ne 0 ]]; then
    exit 1
fi
exit 0
