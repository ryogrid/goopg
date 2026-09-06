#!/usr/bin/env bash
# Provision a fresh local PostgreSQL cluster and start it.
#
#   - initdb creates the cluster under $PGDATA with `postgres` as
#     the bootstrap superuser, matching HammerDB's expected defaults.
#   - The server listens on 127.0.0.1:$PG_PORT only.
#   - pg_hba.conf is rewritten to require scram-sha-256 password auth
#     for TCP connections, so HammerDB's user/password is exercised.
#   - The bootstrap superuser is given a known password.
#
# Idempotent: if a cluster already exists at $PGDATA the script tries
# to start it; pass --reset to wipe and recreate it from scratch.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
source "${SCRIPT_DIR}/env.sh"

reset_data=0
for arg in "$@"; do
    case "${arg}" in
        --reset) reset_data=1 ;;
        *) echo "Unknown argument: ${arg}" >&2; exit 2 ;;
    esac
done

# If the cluster already runs, refuse silently — caller probably forgot
# to stop it. We don't auto-kill because that risks losing in-flight work.
if [[ -f "${PGDATA}/postmaster.pid" ]]; then
    if pg_ctl -D "${PGDATA}" status >/dev/null 2>&1; then
        echo "PostgreSQL already running at ${PGDATA}; run stop_pg.sh first."
        exit 0
    fi
fi

if [[ ${reset_data} -eq 1 && -d "${PGDATA}" ]]; then
    echo "Removing existing data directory: ${PGDATA}"
    rm -rf "${PGDATA}"
fi

# initdb only when we don't already have a populated cluster.
if [[ ! -s "${PGDATA}/PG_VERSION" ]]; then
    mkdir -p "${PGDATA}"
    echo "Running initdb under ${PGDATA}"
    # --pwfile primes the bootstrap superuser's password so we never
    # have to write it to a shell history.
    pwfile="$(mktemp)"
    printf '%s\n' "${PG_SUPERUSER_PASS}" >"${pwfile}"
    chmod 600 "${pwfile}"
    initdb \
        --username="${PG_SUPERUSER}" \
        --auth-host=scram-sha-256 \
        --auth-local=trust \
        --pwfile="${pwfile}" \
        --encoding=UTF8 \
        --locale=C \
        -D "${PGDATA}"
    rm -f "${pwfile}"

    # Tighten pg_hba.conf so password auth is required for TCP, which
    # is what HammerDB will use. Local socket connections stay on
    # `trust` so administrative scripts (psql) can run without a
    # PGPASSWORD round-trip.
    cat >"${PGDATA}/pg_hba.conf" <<HBA
# TYPE  DATABASE        USER            ADDRESS                 METHOD
local   all             all                                     trust
host    all             all             127.0.0.1/32            scram-sha-256
host    all             all             ::1/128                 scram-sha-256
HBA

    # postgresql.conf knobs sized for SF=1 on a developer laptop.
    {
        echo "listen_addresses = '127.0.0.1'"
        echo "port = ${PG_PORT}"
        echo "unix_socket_directories = '${PGSOCKET_DIR}'"
        echo "max_connections = 64"
        # 2048MB to match the goopg cluster (bench/tpch/setup_goopg.sh),
        # raised from 512MB on 2026-09-06. At 512MB against a 2.1 GiB
        # dataset PG held roughly a quarter of the working set while goopg
        # held all of it, so any goopg-vs-PG wall-clock comparison gave
        # goopg 4x the buffer memory. Both engines still benefit from the
        # OS page cache, so the practical gap was smaller than 4x — but it
        # was an asymmetry in the measurement, not in the engines.
        echo "shared_buffers = 2048MB"
        echo "work_mem = 64MB"
        echo "maintenance_work_mem = 256MB"
        echo "effective_cache_size = 2GB"
        # TPC-H queries are read-heavy; larger checkpoints reduce
        # WAL pressure during the bulk load phase.
        echo "checkpoint_timeout = 15min"
        echo "max_wal_size = 4GB"
        echo "wal_compression = on"
        # Allow HammerDB's degree_of_parallel=2 setting to actually
        # spawn parallel workers.
        echo "max_parallel_workers_per_gather = 4"
        echo "max_parallel_workers = 8"
        echo "max_worker_processes = 16"
    } >>"${PGDATA}/postgresql.conf"
fi

echo "Starting PostgreSQL on ${PG_HOST}:${PG_PORT}"
pg_ctl -D "${PGDATA}" -l "${PG_LOG}" -w start

# Wait until the server actually accepts queries before returning.
for _ in $(seq 1 30); do
    if pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
        echo "PostgreSQL ready (log: ${PG_LOG})"
        exit 0
    fi
    sleep 1
done

echo "PostgreSQL did not become ready in time — check ${PG_LOG}" >&2
exit 1
