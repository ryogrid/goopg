#!/usr/bin/env bash
# time TPC-DS SF1 Q74 on a fresh clone of the shipped corpus with one binary.
set -u
REPO=/home/ryo/work/goopg/goopg; BIN="$1"; TAG="$2"
source "${REPO}/bench/tpcds/env_tpcds.sh"
export PATH="${REPO}/postgres/local_install/bin:${PATH}"
SRC="${TPCDS_PGDATA}"; C="${REPO}/tmp/m0229/data"; UNIT=goopg-m0229
[[ -f "${SRC}.HOLD" || -f "${SRC}/postmaster.pid" ]] && { echo "FATAL src"; exit 3; }
pg_isready -h 127.0.0.1 -p 5599 -q 2>/dev/null && { echo "FATAL port busy"; exit 3; }
rm -rf "$C"; cp -a "$SRC" "$C" || exit 3; rm -f "$C/postmaster.pid"
hba=(); [[ -f "$C/pg_hba.conf" ]] && hba=(--hba "$C/pg_hba.conf")
systemctl --user reset-failed "${UNIT}.scope" >/dev/null 2>&1 || true
GOOPG_CG_UNIT="$UNIT" setsid nohup "${REPO}/scripts/goopg-test-run.sh" "$BIN" start -D "$C" \
   --listen 127.0.0.1:5599 "${hba[@]}" >"${REPO}/tmp/m0229/server-${TAG}.log" 2>&1 < /dev/null &
for _ in $(seq 1 180); do pg_isready -h 127.0.0.1 -p 5599 -U postgres -q && break; sleep 1; done
pg_isready -h 127.0.0.1 -p 5599 -U postgres -q || { echo FATAL not ready; exit 5; }
s=$(date +%s.%N)
PGOPTIONS='-c statement_timeout=1200s' timeout 1300 psql -X -q -h 127.0.0.1 -p 5599 -U postgres -d postgres \
  -f "${REPO}/bench/tpcds/runtime_goopg/tpcds-data/queries/query74.sql" > "${REPO}/tmp/m0229/q74-${TAG}.out" 2>&1
rc=$?; e=$(date +%s.%N)
echo "${TAG} rc=${rc} seconds=$(echo "$e - $s" | bc) rows=$(grep -c '' "${REPO}/tmp/m0229/q74-${TAG}.out")"
timeout 60 "$BIN" stop -D "$C" >/dev/null 2>&1; systemctl --user stop "${UNIT}.scope" >/dev/null 2>&1; rm -rf "$C"
