#!/usr/bin/env bash
# capture-stamp.sh — the machine-written provenance stamp for
# scripts/capture-tpch.sh and scripts/capture-tpcds.sh (M0137-0002).
#
# WHY. R122 §10 named the defect this closes in its own words: a capture's
# arm attribution "rests entirely on filename convention" — a human-typed
# <header> string nobody checks against the server that actually answered.
# That is the same class of bug `scripts/planner-flags.sh` and
# `scripts/lib/bench-engine-id.sh` already fixed for the TPC-DS SF0.25 sweep
# (M0127-P5.9-q / 0124-0001 rule D4a): mis-stamped twice, silently, because
# the label was hand-written prose instead of read off the binary. This file
# extends the SAME machinery to the EXPLAIN-plan capture pipeline instead of
# reinventing a second stamp format.
#
# Emits one block, appended to a capture's $OUT right after the caller's own
# `# <header>` line:
#   # engine-id: <trees> diff=<digest>              (bench_engine_id)
#   # repo-head: <git log -1 --oneline>[ [DIRTY]]
#   # planner-flags: VAR=value VAR=value ...          (planner_flags_body)
#   # pinned-GUCs: <caller-supplied description of the session SET list>
#   # engine-binary: pid=<pid> pid-alive=yes|no path=<path> inode=<n> sha=<h>
#     -- or -- engine-binary: UNKNOWN(no datadir given — pass a 6th arg to
#        capture-tpch.sh/capture-tpcds.sh to stamp the serving binary)
#   # stats-epoch: <sha256/16 of `relname,n_live_tup` over pg_stat_user_tables>
#     -- or -- stats-epoch: UNKNOWN(<reason>)
#
# `stats-epoch` is a FINGERPRINT, not a counter: goopg's pg_stat_user_tables
# always reports last_analyze/last_autoanalyze as NULL (PGStatTablesRowsForDBOid,
# internal/catalog/catalog.go — goopg has no incremental pgstat counters), so a
# timestamp cannot serve as the epoch on both arms. n_live_tup IS real on both:
# PG's live counter and goopg's ANALYZE-persisted reltuples (same comment).
# Hashing (relname, n_live_tup) over every user table therefore changes exactly
# when a values sweep re-samples statistics on either engine — which is what
# "stats epoch" means (03-process-retrospective.md §"Stats-epoch drift"). This
# task only stamps it; M0137-0006 makes re-taking the OFF baseline after a
# sweep a CHECKED step instead of a remembered one.
#
# Every field degrades to an explicit UNKNOWN(reason) rather than guessing or
# silently omitting — an honest UNKNOWN costs one attribution, a guessed or
# missing one costs a wrong conclusion (same rule scripts/planner-flags.sh
# states for its own UNKNOWN case).
#
# Usage (sourced by capture-tpch.sh / capture-tpcds.sh, which already set
# REPO_ROOT and PATH-prepend PG_BIN before sourcing this file):
#   source "${SCRIPT_DIR}/lib/capture-stamp.sh"
#   capture_stamp_block "$PORT" "$DB" "$USER" "$DATADIR" "$PIN_DESC" "$(basename "$OUT")"

# shellcheck disable=SC1090,SC1091
_capture_stamp_lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${_capture_stamp_lib_dir}/bench-engine-id.sh"
# shellcheck disable=SC1090,SC1091
source "${_capture_stamp_lib_dir}/../planner-flags.sh"

# _capture_stamp_binary_line <datadir> — the D4a-shaped binary/PID stamp,
# read from <datadir>/postmaster.pid the same way bench_running_engine_sha
# does, but reporting path+inode+liveness directly rather than only a sha —
# the Definition of Done (docs/milestones/0137-...md) names "binary path,
# inode, serving-PID /proc/<pid>/exe verification" as three separate facts,
# not one hash standing in for all three.
_capture_stamp_binary_line() {
    local datadir="$1" pidfile pid path inode alive sha
    if [[ -z "${datadir}" ]]; then
        printf 'UNKNOWN(no datadir given — pass a 6th arg to stamp the serving binary)'
        return 0
    fi
    pidfile="${datadir}/postmaster.pid"
    if [[ ! -f "${pidfile}" ]]; then
        printf 'UNKNOWN(no postmaster.pid under %s)' "${datadir}"
        return 0
    fi
    pid="$(head -1 "${pidfile}")"
    if [[ -z "${pid}" ]]; then
        printf 'UNKNOWN(empty postmaster.pid under %s)' "${datadir}"
        return 0
    fi
    if kill -0 "${pid}" 2>/dev/null; then alive="yes"; else alive="no"; fi
    if [[ -r "/proc/${pid}/exe" ]]; then
        path="$(readlink -f "/proc/${pid}/exe" 2>/dev/null || echo unreadable)"
        inode="$(stat -c %i "/proc/${pid}/exe" 2>/dev/null || echo unreadable)"
        sha="$(sha256sum "/proc/${pid}/exe" 2>/dev/null | cut -c1-16)"
        [[ -z "${sha}" ]] && sha="unreadable"
    else
        path="unreadable"; inode="unreadable"; sha="unreadable"
    fi
    printf 'pid=%s pid-alive=%s path=%s inode=%s sha=%s' "${pid}" "${alive}" "${path}" "${inode}" "${sha}"
}

# _capture_stamp_stats_epoch <port> <db> <user> <tagbase> — fingerprint of
# pg_stat_user_tables(relname, n_live_tup) for the connection's current
# database. Uses -f, never -c: the scratch file name is derived from
# <tagbase> (the capture's own $OUT basename), never from $$, for the same
# K18 reason capture-tpch.sh's header comment gives.
_capture_stamp_stats_epoch() {
    local port="$1" db="$2" user="$3" tagbase="$4" tmp res
    tmp="${TMPDIR:-/tmp}/goopg-parity-stats-epoch-${tagbase}.sql"
    echo "SELECT relname, n_live_tup FROM pg_stat_user_tables ORDER BY relname;" > "${tmp}"
    res="$(timeout 30 psql -h 127.0.0.1 -p "${port}" -U "${user}" -d "${db}" -X -t -A -f "${tmp}" 2>&1)"
    rm -f "${tmp}"
    if [[ -z "${res}" ]]; then
        printf 'UNKNOWN(empty pg_stat_user_tables read)'
        return 0
    fi
    if grep -qi '^psql:\|ERROR' <<<"${res}"; then
        printf 'UNKNOWN(query failed: %s)' "$(head -1 <<<"${res}")"
        return 0
    fi
    printf '%s' "$(sha256sum <<<"${res}" | cut -c1-16)"
}

# capture_stamp_block <port> <db> <user> <datadir> <pin_desc> <tagbase>
# — the full block, newline-terminated, ready to `>> "$OUT"`.
capture_stamp_block() {
    local port="$1" db="$2" user="$3" datadir="$4" pin_desc="$5" tagbase="$6"
    local head
    head="$(cd "${REPO_ROOT}" && git log --oneline -1 2>/dev/null)"
    if [[ -n "$(cd "${REPO_ROOT}" && git status --porcelain 2>/dev/null)" ]]; then
        head="${head} [DIRTY]"
    fi
    printf '# engine-id: %s\n' "$(bench_engine_id)"
    printf '# repo-head: %s\n' "${head}"
    printf '# planner-flags: %s\n' "$(planner_flags_body)"
    printf '# pinned-GUCs: %s\n' "${pin_desc}"
    printf '# engine-binary: %s\n' "$(_capture_stamp_binary_line "${datadir}")"
    printf '# stats-epoch: %s\n' "$(_capture_stamp_stats_epoch "${port}" "${db}" "${user}" "${tagbase}")"
}

# capture_resolve_engine <port> — which engine a capture targets (H6).
# CAPTURE_ENGINE=goopg|pg wins when set. Otherwise the fixed CLAUDE.md port map
# decides for the shared clusters (65432/65438 = PostgreSQL, 65433/65436/65437
# = goopg); any other port is `unknown` (a private clone — set CAPTURE_ENGINE).
capture_resolve_engine() {
    local port="$1"
    case "${CAPTURE_ENGINE:-}" in
        goopg|pg) echo "${CAPTURE_ENGINE}"; return 0 ;;
        "") ;;
        *) echo "invalid"; return 0 ;;
    esac
    case "${port}" in
        65432|65438) echo pg ;;
        65433|65436|65437) echo goopg ;;
        *) echo unknown ;;
    esac
}

# capture_verify_serving_binary <script-name> <port> <datadir>
# — METHODLOGY3 04-actions H6: refuse (return 1, message on stderr) to capture
# goopg from a server whose identity cannot be verified. For goopg:
#   * DATADIR is required (the 6th arg);
#   * <datadir>/postmaster.pid must name a live pid whose listen line matches
#     <port>;
#   * /proc/<pid>/exe must not be "(deleted)" — the image was rebuilt under the
#     running server, so it is no longer the file on disk anyone can name;
#   * GOOPG_EXPECT_BIN_SHA256 is REQUIRED, and sha256(/proc/<pid>/exe) must
#     equal it (a live, non-deleted exe can still be the wrong build).
# PostgreSQL captures are not checked. An `unknown` engine (any non-reference
# port with CAPTURE_ENGINE unset) is REFUSED: a private clone's engine must be
# named explicitly (CAPTURE_ENGINE=goopg|pg), never guessed.
capture_verify_serving_binary() {
    local me="$1" port="$2" datadir="$3" engine pidfile pid exe sha
    engine="$(capture_resolve_engine "${port}")"
    case "${engine}" in
        pg) return 0 ;;
        invalid)
            echo "${me}: CAPTURE_ENGINE='${CAPTURE_ENGINE}' — must be goopg or pg" >&2
            return 1 ;;
        unknown)
            echo "${me}: engine for non-reference port ${port} is unknown — set CAPTURE_ENGINE=goopg|pg explicitly (H6: a goopg capture must verify its serving binary)" >&2
            return 1 ;;
    esac
    if [[ -z "${datadir}" ]]; then
        echo "${me}: goopg capture requires the server's datadir as the 6th arg (H6: serving-binary verification)" >&2
        return 1
    fi
    pidfile="${datadir}/postmaster.pid"
    pid="$(head -1 "${pidfile}" 2>/dev/null || true)"
    if [[ ! "${pid}" =~ ^[0-9]+$ ]] || ! kill -0 "${pid}" 2>/dev/null; then
        echo "${me}: no live postmaster under ${datadir} (pidfile pid='${pid}')" >&2
        return 1
    fi
    if ! grep -qE "(^|:)${port}\$" "${pidfile}" 2>/dev/null; then
        echo "${me}: ${pidfile} does not name port ${port} — datadir and port disagree" >&2
        return 1
    fi
    exe="$(readlink "/proc/${pid}/exe" 2>/dev/null || true)"
    if [[ -z "${exe}" ]]; then
        echo "${me}: cannot read /proc/${pid}/exe" >&2
        return 1
    fi
    if [[ "${exe}" == *" (deleted)" ]]; then
        echo "${me}: serving binary of pid ${pid} is '${exe}' — rebuilt under the running server; restart it on a known binary before capturing" >&2
        return 1
    fi
    if [[ -z "${GOOPG_EXPECT_BIN_SHA256:-}" ]]; then
        echo "${me}: goopg capture requires GOOPG_EXPECT_BIN_SHA256=<sha256 of the binary you mean to measure> (serving pid ${pid}, ${exe})" >&2
        return 1
    fi
    sha="$(sha256sum "/proc/${pid}/exe" 2>/dev/null | awk '{print $1}')"
    if [[ "${sha}" != "${GOOPG_EXPECT_BIN_SHA256}" ]]; then
        echo "${me}: serving binary sha256 ${sha:-unreadable} != GOOPG_EXPECT_BIN_SHA256 ${GOOPG_EXPECT_BIN_SHA256} (pid ${pid}, ${exe})" >&2
        return 1
    fi
    return 0
}
