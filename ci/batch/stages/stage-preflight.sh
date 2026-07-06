#!/bin/bash
# S0 preflight — build + environment checks (ci/design/01 §S0).
# Exits nonzero ONLY on build failure (status fail(build)); environment gaps
# produce skip-notes consumed by later stages / the summarizer, exit 0.
set -uo pipefail
source "${REPO_ROOT}/ci/batch/lib/common.sh"

mkdir -p "${RUN_DIR}/preflight"
CHECKS="${RUN_DIR}/preflight/checks.log"
: > "${CHECKS}"
note() { printf '%s\n' "$*" | tee -a "${CHECKS}"; }

# --- build (the first regression signal) ------------------------------------
progress "S0" "make build start"
if ! make -C "${REPO_ROOT}" build > "${RUN_DIR}/preflight/build.log" 2>&1; then
    progress "S0" "BUILD FAILED — see preflight/build.log"
    stage_status preflight "fail(build)"
    exit 1
fi
progress "S0" "build ok"

# --- environment checks (ok / note, never fail) ------------------------------
free_gb=$(df -BG --output=avail "${REPO_ROOT}" 2>/dev/null | tail -1 | tr -dc '0-9')
if [[ -n "${free_gb}" && "${free_gb}" -lt 10 ]]; then
    note "disk: LOW (${free_gb}G free < 10G)"
else
    note "disk: ok (${free_gb:-?}G free)"
fi

for bin in psql pgbench pg_isready; do
    if command -v "${bin}" >/dev/null 2>&1; then
        note "binary ${bin}: ok ($(command -v "${bin}"))"
    elif [[ -x "${REPO_ROOT}/postgres/local_install/bin/${bin}" ]]; then
        note "binary ${bin}: ok (postgres/local_install/bin)"
    else
        note "binary ${bin}: MISSING (dependent tests will t.Skip)"
    fi
done

TPCH_DATA="${REPO_ROOT}/bench/tpch/runtime_goopg/data"
if [[ -s "${TPCH_DATA}/PG_VERSION" ]]; then
    mb=$(du -sm "${TPCH_DATA}" 2>/dev/null | awk '{print $1}')
    if [[ -n "${mb}" && "${mb}" -ge 100 ]]; then
        note "tpch-data: ok (${mb} MB)"
    else
        note "tpch-data: TOO SMALL (${mb:-0} MB < 100) — S2 will skip(no-data)"
    fi
else
    note "tpch-data: ABSENT — S2 will skip(no-data)"
fi

if port_busy 127.0.0.1 65433; then
    note "port 65433 (canonical/loop lane): BUSY — S2 only needs it free for the snapshot-copy window (waits up to \${NIGHTLY_PORT_WAIT}s)"
else
    note "port 65433 (canonical/loop lane): free"
fi
if port_busy 127.0.0.1 "${NIGHTLY_TPCH_PORT:-65434}"; then
    note "port ${NIGHTLY_TPCH_PORT:-65434} (batch TPC-H clone lane): BUSY — S2 will skip(port-busy)"
else
    note "port ${NIGHTLY_TPCH_PORT:-65434} (batch TPC-H clone lane): free"
fi

progress "S0" "preflight checks done"
stage_status preflight "pass"
exit 0
