#!/usr/bin/env bash
# Private TPC-H arm for the Track-E session: port 5535, private clone,
# private binary. Never touches 65433/65437.
set -uo pipefail
REPO_ROOT=/home/ryo/work/goopg/goopg
source "${REPO_ROOT}/scripts/lib/bench-engine-id.sh"
ARM="${1:?arm}"; OUT="${2:?out}"; shift 2
PG_PREFIX="${REPO_ROOT}/postgres/local_install"
export LD_LIBRARY_PATH="${PG_PREFIX}/lib${LD_LIBRARY_PATH:+:${LD_LIBRARY_PATH}}"
export PATH="${PG_PREFIX}/bin:${PATH}"
PG_HOST=127.0.0.1
PG_PORT=5535
PGDATA="${REPO_ROOT}/tmp/e14-tpch-data"
GOOPG_BIN="${GOOPG_BIN:-${REPO_ROOT}/tmp/goopg-tracke-bin}"
RUNNER_BIN="${RUNNER_BIN:-${REPO_ROOT}/tmp/tpch-tracke-runner}"
CG_UNIT="goopg-tracke-${ARM}"
SRV_LOG="${SRV_LOG:-${REPO_ROOT}/tmp/tracke-${ARM}.server.log}"
PER_Q="${PER_Q:-600}"
QUERIES="${QUERIES:-}"
DIGEST="${DIGEST:-1}"
export GOMEMLIMIT="${GOMEMLIMIT:-12GiB}" GOGC="${GOGC:-off}"
export GOOPG_ANALYZE_SEED="${GOOPG_ANALYZE_SEED:-20260905}"
export GOOPG_MEM_HIGH="${GOOPG_MEM_HIGH:-20G}" GOOPG_MEM_MAX="${GOOPG_MEM_MAX:-24G}"
export GOOPG_MEM_SWAP_MAX="${GOOPG_MEM_SWAP_MAX:-0}"
export GOOPG_PGSHAPED_DP="${PGSHAPED:-0}"
export GOOPG_PGSHAPED_COLLAPSE="${COLLAPSE:-0}"

if pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -q 2>/dev/null; then
    echo "something already on ${PG_PORT}" >&2; exit 3
fi
[[ -s "${PGDATA}/PG_VERSION" ]] || { echo "no cluster at ${PGDATA}" >&2; exit 3; }
mkdir -p "${REPO_ROOT}/tmp"
if [[ "${NO_BUILD:-0}" != "1" ]]; then
    ( cd "${REPO_ROOT}" && go build -o "${GOOPG_BIN}" ./cmd/goopg ) || exit 4
    ( cd "${REPO_ROOT}" && go build -o "${RUNNER_BIN}" ./cmd/tpch-runner ) || exit 4
fi
"${GOOPG_BIN}" stop -D "${PGDATA}" >/dev/null 2>&1 || true
systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true

GOOPG_CG_UNIT="${CG_UNIT}" "${REPO_ROOT}/scripts/goopg-test-run.sh" \
    "${GOOPG_BIN}" start -D "${PGDATA}" --listen "${PG_HOST}:${PG_PORT}" \
    --hba "${PGDATA}/pg_hba.conf" >"${SRV_LOG}" 2>&1 &
server_pid=$!
cleanup() {
    timeout 60 "${GOOPG_BIN}" stop -D "${PGDATA}" >>"${SRV_LOG}" 2>&1 || true
    wait "${server_pid}" 2>/dev/null || true
    systemctl --user stop "${CG_UNIT}.scope" >/dev/null 2>&1 || true
    systemctl --user reset-failed "${CG_UNIT}.scope" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT TERM
ready=0
for _ in $(seq 1 300); do
    kill -0 "${server_pid}" 2>/dev/null || break
    pg_isready -h "${PG_HOST}" -p "${PG_PORT}" -U postgres -q >/dev/null 2>&1 && { ready=1; break; }
    sleep 1
done
[[ "${ready}" -eq 1 ]] || { echo "FATAL: arm ${ARM} server not ready"; tail -30 "${SRV_LOG}"; exit 5; }
runner_args=(-host "${PG_HOST}" -port "${PG_PORT}" -db tpch -user tpch -password tpch
             -per-query-timeout "${PER_Q}s")
[[ -n "${QUERIES}" ]] && runner_args+=(-queries "${QUERIES}")
[[ "${DIGEST}" == "1" ]] && runner_args+=(-digest)
{
    echo "# arm=${ARM} port=${PG_PORT} data=${PGDATA}"
    echo "# started $(date -Is)"
    echo "# engine-id: $(bench_engine_id)"
    echo "# engine-binary: on-disk=$(bench_engine_bin_sha "${GOOPG_BIN}") (${GOOPG_BIN#"${REPO_ROOT}/"})"
    echo "# per-query cap ${PER_Q}s, serial, digest=${DIGEST}, queries=${QUERIES:-all}"
    echo "# host: load$(cut -d' ' -f1-3 /proc/loadavg)"
} >"${OUT}"
"${RUNNER_BIN}" "${runner_args[@]}" "$@" >>"${OUT}" 2>&1
echo "# finished $(date -Is)" >>"${OUT}"
echo "arm ${ARM} written: ${OUT} ($(wc -l <"${OUT}") lines)"
