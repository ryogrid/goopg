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
# The snapshot never stops/starts anything on SRC_DATA/SRC_PORT — it only
# waits for that port to go quiet (whoever is using it keeps ownership) and
# copies the on-disk files once that is true.

# tpch_wait_port_free <host> <port> <timeout-s> — poll pg_isready until it
# reports the port has no answering server, or return 1 after <timeout-s>.
# Independent re-implementation of ci/batch/lib/common.sh's wait_port_free
# (deliberately not sourced from there — see the file header).
tpch_wait_port_free() {
    local host="$1" port="$2" timeout="${3:-60}" waited=0
    while pg_isready -h "${host}" -p "${port}" -q 2>/dev/null; do
        if (( waited >= timeout )); then
            return 1
        fi
        sleep 1
        waited=$(( waited + 1 ))
    done
    return 0
}

# tpch_private_clone_snapshot <src-data> <dst-data> <host> <src-port> [wait-timeout-s]
#
# Snapshot-copies <src-data> (the shared, lane-external canonical dir) into
# <dst-data> (this lane's private clone) WITHOUT ever touching a server on
# <src-data>. Up to 3 attempts, mirroring ci/batch/stages/stage-tpch.sh's
# defence against the same hazard: wait for <src-port> to answer nothing
# (up to [wait-timeout-s], default 60), `cp -a`, then verify the port is
# STILL free (a server could have started in the gap) — retry on
# interference, never on an unconditioned "just copy it" (a copy taken while
# a server is writing risks a torn/inconsistent data dir).
#
# On success: <dst-data>/postmaster.pid is removed (a copied stale pidfile
# would block `start -D` on the clone) and returns 0. On failure, <dst-data>
# is left absent (never a half-written copy) and returns non-zero with a
# message on stderr:
#   3 = <src-data> has no initialised cluster (missing PG_VERSION)
#   4 = `cp -a` itself failed (disk space?)
#   5 = could not get an interference-free copy in 3 attempts
tpch_private_clone_snapshot() {
    local src="$1" dst="$2" host="$3" src_port="$4" wait_timeout="${5:-60}"
    [[ -s "${src}/PG_VERSION" ]] || {
        echo "tpch-private-clone: no initialised cluster at ${src}" >&2
        return 3
    }
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
