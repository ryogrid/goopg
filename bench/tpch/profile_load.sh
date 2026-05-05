#!/usr/bin/env bash
# profile_load.sh wraps the hammerdb_load smoke run with pprof
# captures from goopg's `-pprof-listen` endpoint. The resulting
# profiles drive the M0032-0005 slice-2 fixes (top 2-3 hotspots in
# the batched-INSERT path).
#
# Pre-conditions:
#   - goopg cluster is running with `-pprof-listen 127.0.0.1:6060`
#     (setup_goopg.sh adds this to the start command).
#   - The schema has NOT been built yet (the loader will issue
#     CREATE TABLE itself, matching HammerDB's CreateTables).

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=env_goopg.sh
source "${SCRIPT_DIR}/env_goopg.sh"

LIMIT_ORDERS="${LIMIT_ORDERS:-100000}"
PPROF_PORT="${PPROF_PORT:-6060}"
PPROF_SECS="${PPROF_SECS:-120}"
ts="$(date +%Y%m%d-%H%M%S)"
out_dir="${SCRIPT_DIR}/profiles/${ts}"
mkdir -p "${out_dir}"

if ! curl -sf "http://127.0.0.1:${PPROF_PORT}/debug/pprof/" >/dev/null; then
    echo "goopg pprof endpoint not reachable on :${PPROF_PORT}; start goopg with -pprof-listen 127.0.0.1:${PPROF_PORT}" >&2
    exit 1
fi

# Build the loader binary into the runtime tree.
loader_bin="${RUNTIME_DIR}/hammerdb_load"
( cd "${REPO_ROOT}" && go build -o "${loader_bin}" ./bench/tpch/cmd/hammerdb_load )

echo "Profiles → ${out_dir}"
echo "Running loader with --limit-orders ${LIMIT_ORDERS}"

# Kick off the loader in the background, wait a few seconds for
# steady-state, then sample CPU + heap.
(
    "${loader_bin}" \
        --addr "${PG_HOST}:${PG_PORT}" \
        --user "${PG_SUPERUSER}" \
        --db postgres \
        --batch-rows 10 \
        --commit-interval 100 \
        --limit-orders "${LIMIT_ORDERS}" \
        2>&1 | tee "${out_dir}/loader.log"
) &
loader_pid=$!

# Let the load reach steady state before we snapshot.
sleep 10

echo "Capturing CPU profile (${PPROF_SECS}s)..."
curl -s "http://127.0.0.1:${PPROF_PORT}/debug/pprof/profile?seconds=${PPROF_SECS}" \
    -o "${out_dir}/cpu.pprof" &
cpu_pid=$!

echo "Capturing heap snapshot..."
curl -s "http://127.0.0.1:${PPROF_PORT}/debug/pprof/heap" \
    -o "${out_dir}/heap.pprof"

echo "Capturing goroutine snapshot..."
curl -s "http://127.0.0.1:${PPROF_PORT}/debug/pprof/goroutine" \
    -o "${out_dir}/goroutine.pprof"

wait "${cpu_pid}"
echo "CPU profile captured: $(ls -lh "${out_dir}/cpu.pprof" | awk '{print $5}')"

# Wait for the loader to finish (or be killed by HammerDB-style
# timeout, in which case we still have a profile from steady state).
wait "${loader_pid}" || echo "loader exited non-zero (drop / timeout?)"

echo "Top-30 CPU functions:"
go tool pprof -top -nodecount=30 "${out_dir}/cpu.pprof" 2>/dev/null | tee "${out_dir}/cpu_top.txt"

echo "Top-30 heap allocators:"
go tool pprof -top -nodecount=30 "${out_dir}/heap.pprof" 2>/dev/null | tee "${out_dir}/heap_top.txt"

echo "Profile dir: ${out_dir}"
