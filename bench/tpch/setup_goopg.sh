#!/usr/bin/env bash
# Provision a fresh local goopg cluster and start it for HammerDB
# TPC-H benchmarking. Mirrors setup_pg.sh's role for the upstream
# variant: build the binary, init a data directory, drop a
# postgresql.conf + pg_hba.conf tailored to a loopback HammerDB
# run, and start the server in the background.
#
# Idempotent: re-running without --reset just (re)starts the
# existing cluster. --reset wipes the data directory first.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env_goopg.sh
source "${SCRIPT_DIR}/env_goopg.sh"

reset_data=0
for arg in "$@"; do
    case "${arg}" in
        --reset) reset_data=1 ;;
        *) echo "Unknown argument: ${arg}" >&2; exit 2 ;;
    esac
done

# Build the goopg binary so a clean checkout can run the bench
# without a prior `go build`. Cached re-builds are cheap.
echo "Building goopg → ${GOOPG_BIN}"
mkdir -p "$(dirname "${GOOPG_BIN}")"
( cd "${REPO_ROOT}" && go build -o "${GOOPG_BIN}" ./cmd/goopg )

# Refuse silently if a goopg cluster is already running here.
if [[ -f "${PGDATA}/postmaster.pid" ]]; then
    if "${GOOPG_BIN}" status -D "${PGDATA}" >/dev/null 2>&1; then
        echo "goopg already running at ${PGDATA}; run stop_goopg.sh first."
        exit 0
    fi
fi

if [[ ${reset_data} -eq 1 && -d "${PGDATA}" ]]; then
    echo "Removing existing data directory: ${PGDATA}"
    rm -rf "${PGDATA}"
fi

if [[ ! -s "${PGDATA}/PG_VERSION" ]]; then
    mkdir -p "${PGDATA}"
    echo "Running goopg init under ${PGDATA}"
    "${GOOPG_BIN}" init -D "${PGDATA}"

    # postgresql.conf knobs sized for SF=1 TPC-H.
    # shared_buffers=2GiB: arena fits in Go heap under GC control
    # (M0032-0001). Paired with GOMEMLIMIT=20GiB and explicit
    # runtime.GC() after query/COPY completion (M0032-0006).
    {
        echo "listen_addresses = '127.0.0.1'"
        echo "port = ${PG_PORT}"
        echo "shared_buffers = 2048MB"
        echo "checkpoint_timeout = 15min"
        echo "max_wal_size = 4GB"
    } >>"${PGDATA}/postgresql.conf"

    # HammerDB connects with user=postgres password=postgres. Goopg's
    # default policy trusts loopback regardless of the password, so
    # the conn just works. We still write an explicit pg_hba so the
    # behaviour is documented and future scram-sha-256 enforcement
    # has a clear toggle point.
    cat >"${PGDATA}/pg_hba.conf" <<HBA
# TYPE  DATABASE        USER            ADDRESS                 METHOD
local   all             all                                     trust
host    all             all             127.0.0.1/32            trust
host    all             all             ::1/128                 trust
HBA
fi

echo "Starting goopg on ${PG_HOST}:${PG_PORT}"
nohup "${GOOPG_BIN}" start -D "${PGDATA}" \
    --listen "${PG_HOST}:${PG_PORT}" \
    --hba "${PGDATA}/pg_hba.conf" \
    >"${PG_LOG}" 2>&1 &
echo "$!" >"${RUNTIME_DIR}/goopg.pid"

# Wait until the server actually accepts queries before returning.
for _ in $(seq 1 30); do
    if pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U "${PG_SUPERUSER}" >/dev/null 2>&1; then
        echo "goopg ready (log: ${PG_LOG})"
        exit 0
    fi
    sleep 1
done

echo "goopg did not become ready in time — check ${PG_LOG}" >&2
exit 1
