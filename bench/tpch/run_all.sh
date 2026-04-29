#!/usr/bin/env bash
# End-to-end TPC-H benchmark driver:
#   1. (Re)create a fresh local PostgreSQL cluster under
#      bench/tpch/runtime/pgdata.
#   2. Build the TPC-H schema with scale factor 1 via HammerDB.
#   3. Run the 22-query power test, again via HammerDB.
#   4. Stop the cluster.
#
# Pass --keep-running to skip step 4 (useful for ad-hoc inspection
# of the loaded database with psql afterwards).

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env.sh
source "${SCRIPT_DIR}/env.sh"

keep_running=0
for arg in "$@"; do
    case "${arg}" in
        --keep-running) keep_running=1 ;;
        *) echo "Unknown argument: ${arg}" >&2; exit 2 ;;
    esac
done

cleanup() {
    if [[ ${keep_running} -eq 0 ]]; then
        "${SCRIPT_DIR}/stop_pg.sh" || true
    else
        echo "Cluster left running on ${PG_HOST}:${PG_PORT} (PGDATA=${PGDATA})"
    fi
}
trap cleanup EXIT

"${SCRIPT_DIR}/setup_pg.sh" --reset
"${SCRIPT_DIR}/build_schema.sh"
"${SCRIPT_DIR}/run_power_test.sh"

echo "TPC-H benchmark finished."
