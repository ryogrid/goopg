#!/usr/bin/env bash
#
# tpch-private-clone.sh — M0137-0007: give a TPC-H gate/measurement lane its
# own private data-dir clone and private port instead of `stop -D`/`start -D`
# on the shared bench cluster (bench/tpch/runtime_goopg/data, canonical port
# :65433).
#
# Why this exists: AGENT.md §"Plan-parity harness" names shared-resource
# contention on :65433 as "the stated reason for most deferred gates" in the
# previous phase — concretely, scripts/tpch-spotcheck.sh, tpch-relsize-arm.sh,
# tpch-estimate-audit-arm.sh and tpch-acceptance-arm.sh each independently
# `stop -D`/`start -D` the SAME shared PGDATA/port. Two of them running
# concurrently (or one running while an M0137 baseline capture is mid-session
# against the live cluster) fight over it: whichever starts second kills the
# first's server out from under it. ci/batch/stages/stage-tpch.sh already
# solved exactly this problem for the nightly lane with a snapshot-copy onto
# its own reserved port (65434, see ci/design/03-resources-and-parallelism.md
# §D and ci/design/05-tpch-stage.md); this library generalises that pattern
# for the four loop-facing scripts above, each landing on its own 55xx port
# (see the table in AGENT.md's M0137-0007 write-up / the individual scripts)
# rather than reusing ci/batch's reserved 65434/65435. It is intentionally
# independent of ci/batch/lib/common.sh: a loop-facing gate must work in a
# checkout that never runs ci/batch.
#
# ---------------------------------------------------------------------------
# ONLINE CLONE (the M0139 follow-up fix)
# ---------------------------------------------------------------------------
# The original M0137-0007 shape had exactly ONE way to obtain the snapshot:
# wait for :65433 to answer nothing, then `cp -a`. That precondition is not
# "the shared cluster is momentarily idle", it is "the shared cluster is NOT
# RUNNING AT ALL" — and per CLAUDE.md's port table :65433 is a *persistent*
# bench cluster that stays up for days. So the private-port half of M0137-0007
# landed while the snapshot step kept a hard dependency on stopping the very
# server the task existed to protect. M0139-S1/S2 paid the bill: three
# retries / ~15 minutes, every attempt dying with
# `127.0.0.1:65433 still busy after 60s`, and two production planner changes
# landing with the TPC-H values gate unverified
# (docs/design/0100-0149/m0139-s1-join-leg-hook.md §"Gates run").
#
# The fix is to stop treating "a server is up" as un-snapshottable. PostgreSQL
# has a first-class answer for cloning a RUNNING cluster consistently, and
# goopg implements the server side of it: the BASE_BACKUP replication command
# (internal/backup/basebackup.go, M0102-0001/-0007 + M0095-0003), driven by
# the bundled `pg_basebackup`. Its consistency contract is the standard
# online-backup one, not a hope that nothing was being written:
#
#   * BASE_BACKUP forces a synchronous IMMEDIATE checkpoint up front and
#     reports that checkpoint's REDO LSN as the start LSN;
#   * `-X fetch` ships every WAL segment from the redo point to the stop LSN
#     inside the same archive, so the clone carries the WAL that makes the
#     copied pages consistent;
#   * `global/pg_control` is emitted LAST and is patched to name that
#     checkpoint, so the clone's first start replays from the redo point
#     ("database system was not properly shut down; automatic recovery in
#     progress") and reaches a consistent state before accepting connections.
#
# So the resolution order is now:
#
#   port answers nothing  ->  `cp -a` (cheapest; the dir is genuinely at rest)
#   port answers          ->  pg_basebackup -X fetch (no stop, no wait)
#   online path failed    ->  fall back to the quiesce-then-`cp -a` loop,
#                             which still REFUSES rather than copy mid-write
#
# The shared cluster is still never stopped, never started, and never has its
# data dir written by this library. The online path does make the shared
# server do work it would not otherwise do (one immediate checkpoint plus the
# read of ~2 GB): that is a load perturbation, not a correctness one, and is
# strictly less invasive than the `stop -D` this whole library replaced. A
# lane that must not perturb the shared server at all can still force the old
# behaviour with TPCH_CLONE_MODE=copy.
#
# Usage (after sourcing bench/tpch/env_goopg.sh, which sets PGDATA/PG_PORT to
# the shared canonical values — capture those as SRC_* BEFORE overriding
# PGDATA/PG_PORT to the lane's private clone):
#
#   source "${REPO_ROOT}/bench/tpch/env_goopg.sh"
#   source "${REPO_ROOT}/scripts/lib/tpch-private-clone.sh"
#   SRC_DATA="${PGDATA}"; SRC_PORT="${PG_PORT}"
#   PGDATA="${REPO_ROOT}/tmp/goopg-<lane>-tpch-data"; PG_PORT=<558x>
#   tpch_private_clone_snapshot "${SRC_DATA}" "${PGDATA}" "${PG_HOST}" "${SRC_PORT}" \
#       || exit 1   # or SKIP, per the caller's own no-data convention
#   trap '... your server-stop logic ...; rm -rf "${PGDATA}"' EXIT
#
# Tunables (all optional):
#   TPCH_CLONE_MODE            auto (default) | online | copy
#                              auto  = the resolution order described above
#                              online= pg_basebackup only, never `cp -a`
#                              copy  = the pre-M0139 quiesce-then-copy only
#   TPCH_CLONE_ONLINE_TIMEOUT  seconds allowed for one pg_basebackup   (900)
#   TPCH_CLONE_USER            replication-connection role    (PGUSER/postgres)
#   TPCH_CLONE_BASEBACKUP_OPTS extra pg_basebackup flags               (empty)

# tpch_wait_port_free <host> <port> <timeout-s> — poll pg_isready until it
# reports the port has no answering server, or return 1 after <timeout-s>.
# Independent re-implementation of ci/batch/lib/common.sh's wait_port_free
# (deliberately not sourced from there — see the file header).
tpch_wait_port_free() {
    local host="$1" port="$2" timeout="${3:-60}" waited=0
    # Without pg_isready there is no liveness probe at all, and the loop below
    # would exit on its first iteration and report the port FREE. The caller
    # then `cp -a`s what may be a running cluster's data directory — a torn
    # copy, silently. Refuse instead. This matters because the usual way to be
    # missing pg_isready is to have skipped the PATH export, which also hides
    # pg_basebackup and so routes `auto` into exactly this fallback.
    if ! command -v pg_isready >/dev/null 2>&1; then
        echo "tpch_wait_port_free: pg_isready not on PATH — cannot prove ${host}:${port} is free; refusing." >&2
        echo "  export PATH=\"\${REPO_ROOT}/postgres/local_install/bin:\${PATH}\" (see AGENT.md, M0137-0003)" >&2
        return 1
    fi
    while pg_isready -h "${host}" -p "${port}" -q 2>/dev/null; do
        if (( waited >= timeout )); then
            return 1
        fi
        sleep 1
        waited=$(( waited + 1 ))
    done
    return 0
}

# tpch_clone_online_available <host> <port> — 0 when the online (BASE_BACKUP)
# path is usable right now: `pg_basebackup` is on PATH and a server answers
# on <host>:<port>. Callers use it to decide which path to try first; it is
# never a correctness gate (the online path validates its own result).
tpch_clone_online_available() {
    local host="$1" port="$2"
    command -v pg_basebackup >/dev/null 2>&1 || return 1
    pg_isready -h "${host}" -p "${port}" -q 2>/dev/null || return 1
    return 0
}

# tpch_private_clone_online <dst-data> <host> <src-port> — clone the RUNNING
# server on <host>:<src-port> into <dst-data> via `pg_basebackup -X fetch`,
# without stopping it or writing anything into its data dir. See the file
# header for why this is consistent. Returns 0 on success (with <dst-data>
# populated and free of a stale postmaster.pid), non-zero on any failure with
# <dst-data> removed — never a half-written copy.
tpch_private_clone_online() {
    local dst="$1" host="$2" src_port="$3"
    local user="${TPCH_CLONE_USER:-${PGUSER:-postgres}}"
    local tmo="${TPCH_CLONE_ONLINE_TIMEOUT:-900}"
    local extra="${TPCH_CLONE_BASEBACKUP_OPTS:-}"

    command -v pg_basebackup >/dev/null 2>&1 || {
        echo "tpch-private-clone: pg_basebackup not on PATH — online clone unavailable (source bench/tpch/env_goopg.sh, which puts postgres/local_install/bin on PATH)" >&2
        return 6
    }

    rm -rf "${dst}"
    mkdir -p "$(dirname "${dst}")"

    # --no-sync: the clone is a throwaway under tmp/ that every run re-creates
    # from scratch, so fsyncing 2 GB buys nothing but wall clock.
    # --no-manifest: goopg supports the manifest, but it costs a full checksum
    # pass over the archive and no lane reads it.
    # -X fetch: NOT optional — it is what makes the copy consistent.
    # shellcheck disable=SC2086
    if ! timeout "${tmo}" pg_basebackup \
            -h "${host}" -p "${src_port}" -U "${user}" \
            -D "${dst}" -X fetch --no-manifest --no-sync ${extra} >&2; then
        echo "tpch-private-clone: pg_basebackup -h ${host} -p ${src_port} failed (see above)" >&2
        rm -rf "${dst}"
        return 7
    fi
    if [[ ! -s "${dst}/PG_VERSION" ]]; then
        echo "tpch-private-clone: pg_basebackup produced no PG_VERSION in ${dst}" >&2
        rm -rf "${dst}"
        return 7
    fi
    # BASE_BACKUP excludes postmaster.pid server-side; belt and braces.
    rm -f "${dst}/postmaster.pid"
    return 0
}

# tpch_private_clone_copy <src-data> <dst-data> <host> <src-port> [wait-timeout-s]
#
# The original (pre-M0139) offline path, unchanged in behaviour: up to 3
# attempts of "wait for <src-port> to answer nothing (up to [wait-timeout-s],
# default 60), `cp -a`, verify the port is STILL free (a server could have
# started in the gap)". It never copies while a server is up, because a `cp
# -a` taken under a live writer risks a torn/inconsistent data dir. It never
# stops or starts anything on <src-data>/<src-port>.
tpch_private_clone_copy() {
    local src="$1" dst="$2" host="$3" src_port="$4" wait_timeout="${5:-60}"
    local attempt
    for attempt in 1 2 3; do
        if ! tpch_wait_port_free "${host}" "${src_port}" "${wait_timeout}"; then
            echo "tpch-private-clone: ${host}:${src_port} still busy after ${wait_timeout}s — refusing to snapshot mid-write (attempt ${attempt}/3)" >&2
            continue
        fi
        rm -rf "${dst}"
        mkdir -p "$(dirname "${dst}")"
        if ! cp -a "${src}" "${dst}"; then
            rm -rf "${dst}"
            echo "tpch-private-clone: cp -a ${src} -> ${dst} failed (disk space?)" >&2
            return 4
        fi
        if pg_isready -h "${host}" -p "${src_port}" -q 2>/dev/null; then
            echo "tpch-private-clone: a server appeared on ${host}:${src_port} mid-copy — snapshot may be inconsistent; retrying (attempt ${attempt}/3)" >&2
            rm -rf "${dst}"
            continue
        fi
        rm -f "${dst}/postmaster.pid"
        return 0
    done
    echo "tpch-private-clone: could not get an interference-free copy of ${src} in 3 attempts" >&2
    rm -rf "${dst}"
    return 5
}

# tpch_private_clone_snapshot <src-data> <dst-data> <host> <src-port> [wait-timeout-s]
#
# Snapshot the shared, lane-external cluster into <dst-data> (this lane's
# private clone) WITHOUT ever stopping, starting, or writing to <src-data>.
# Picks the online (pg_basebackup) or offline (`cp -a`) path per
# TPCH_CLONE_MODE — see the file header for the resolution order and the
# consistency argument for each.
#
# On success <dst-data> is a startable data dir with no stale postmaster.pid
# and returns 0. On failure <dst-data> is left absent (never a half-written
# copy) and a message goes to stderr:
#   3 = <src-data> has no initialised cluster (missing PG_VERSION)
#   4 = `cp -a` itself failed (disk space?)
#   5 = could not get an interference-free copy in 3 attempts
#   6 = TPCH_CLONE_MODE=online but pg_basebackup is unavailable
#   7 = TPCH_CLONE_MODE=online and pg_basebackup failed
tpch_private_clone_snapshot() {
    local src="$1" dst="$2" host="$3" src_port="$4" wait_timeout="${5:-60}"
    [[ -s "${src}/PG_VERSION" ]] || {
        echo "tpch-private-clone: no initialised cluster at ${src}" >&2
        return 3
    }

    local mode="${TPCH_CLONE_MODE:-auto}"
    case "${mode}" in
        online)
            echo "tpch-private-clone: mode=online — pg_basebackup from ${host}:${src_port}" >&2
            tpch_private_clone_online "${dst}" "${host}" "${src_port}"
            return $?
            ;;
        copy)
            echo "tpch-private-clone: mode=copy — waiting for ${host}:${src_port} to go quiet, then cp -a" >&2
            tpch_private_clone_copy "${src}" "${dst}" "${host}" "${src_port}" "${wait_timeout}"
            return $?
            ;;
        auto) ;;
        *)
            echo "tpch-private-clone: unknown TPCH_CLONE_MODE=${mode} (want auto|online|copy)" >&2
            return 2
            ;;
    esac

    # auto: a live server on the source port is the COMMON case (:65433 is a
    # persistent cluster), and it is exactly the case the offline path cannot
    # serve. Take the online route then; keep the cheap `cp -a` for a source
    # that is genuinely at rest.
    if tpch_clone_online_available "${host}" "${src_port}"; then
        echo "tpch-private-clone: ${host}:${src_port} is live — cloning it online via pg_basebackup -X fetch (the shared server is NOT stopped)" >&2
        if tpch_private_clone_online "${dst}" "${host}" "${src_port}"; then
            return 0
        fi
        echo "tpch-private-clone: online clone failed — falling back to the quiesce-then-copy path" >&2
    fi
    tpch_private_clone_copy "${src}" "${dst}" "${host}" "${src_port}" "${wait_timeout}"
}
