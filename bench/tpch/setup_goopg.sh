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
        # take2 P0-12: match the PG reference cluster's PLANNER-VISIBLE memory
        # settings, so a goopg-vs-PG comparison measures the planner rather than
        # a memory budget. Before this, PG ran with work_mem=64MB /
        # effective_cache_size=2GB while goopg left both at its boot defaults
        # (512MB / 4GB) — an 8x work_mem advantage to goopg, which made every
        # work_mem-sensitive cost comparison between the engines meaningless.
        #
        # Only meaningful since P2-01/P2-02: before those, work_mem reached the
        # EXECUTOR but not the planner, so setting it here would have made the
        # two disagree — the hazard cost_funcs.go's workMem comment names.
        #
        # shared_buffers is deliberately NOT aligned. goopg's buffer arena is a
        # Go-heap object under GOMEMLIMIT (M0032-0001); shrinking it to PG's
        # 512MB would measure Go's GC behaviour, not the planner. It is recorded
        # as a permitted divergence rather than drift.
        echo "work_mem = 64MB"
        echo "effective_cache_size = 2GB"
        # M0057-0002: suppress mid-benchmark checkpoints. 24-hour time
        # threshold and 1 TiB WAL threshold are both unreachable in a
        # 2-hour SF=1 power test. A manual CHECKPOINT is issued before
        # the power test starts (M0054-0007-checkpoint-before-run).
        echo "checkpoint_timeout = 24h"
        echo "max_wal_size = 1024GB"
        # autovacuum: ON, to match PostgreSQL's default and the PG reference
        # cluster (2026-09-06). CROSS-ENGINE FAIRNESS OVERRIDES GATE
        # DETERMINISM here, deliberately, and the cost is real — read on
        # before changing it back.
        #
        # This was `off` from 2026-09-05, for a measured reason: the
        # autovacuum launcher runs a SAMPLED ANALYZE every
        # autovacuum_naptime (60 s), so on a cluster used as a measurement
        # instrument the planner's inputs move DURING a run. Statistics
        # changed between the two arms of an A/B and even between two
        # queries of one arm, flipping whole join methods (Q3 Nested Loop vs
        # Merge Join, Q9 hash vs merge spine). Turning it off was one of the
        # three fixes that took A/A plan-capture noise from 455 estimate
        # lines and 27 shape lines to ZERO, which is what made the
        # cost-exact plan pin (`make plan-gate MODE=costs`) possible at all.
        # See docs/design/planner-gate-reproducibility/DESIGN.md.
        #
        # Turning it back on therefore re-exposes that drift. The two needs
        # are separable and should be kept separate:
        #   - a goopg-vs-PG TIMING arm wants autovacuum on, so both engines
        #     run the same maintenance policy;
        #   - a goopg-internal PLAN-CAPTURE or plan-pin arm wants it off, or
        #     wants an explicit in-session `ANALYZE <table>` per table with
        #     GOOPG_ANALYZE_SEED set, because it is comparing two goopg
        #     builds and any stats movement is pure noise.
        # If A/A plan noise returns, that is the expected mechanism and the
        # answer is to pin stats for that arm, NOT to re-disable autovacuum
        # for every measurement.
        #
        # Side effect that returns with it, and is a benefit: autovacuum
        # sets visibility-map bits again, so index-only paths stop being
        # priced pessimistically. The "run one manual VACUUM after a fresh
        # load" workaround that `off` required is no longer needed.
        echo "autovacuum = on"
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
