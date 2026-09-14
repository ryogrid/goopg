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
