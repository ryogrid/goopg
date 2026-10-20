#!/usr/bin/env bash
# start1.sh: (re)start the private SF1 clone on 5598 (copies the corpus only when data1 is absent).
set -u
REPO=/home/ryo/work/goopg/goopg
source "${REPO}/bench/tpcds/env_tpcds.sh"
export PATH="${REPO}/postgres/local_install/bin:${PATH}"
SRC="${TPCDS_PGDATA}"; C="${REPO}/tmp/m0231/data1"; BIN="${GOBIN_OVERRIDE:-${REPO}/tmp/m0214s3/goopg}"; UNIT=goopg-m0231s1
pg_isready -h 127.0.0.1 -p 5598 -q 2>/dev/null && { timeout 60 "$BIN" stop -D "$C" >/dev/null 2>&1; sleep 2; }
systemctl --user stop "${UNIT}.scope" 2>/dev/null; systemctl --user reset-failed "${UNIT}.scope" >/dev/null 2>&1 || true
if [[ ! -d "$C" ]]; then
  [[ -f "${SRC}.HOLD" || -f "${SRC}/postmaster.pid" ]] && { echo "FATAL src"; exit 3; }
  cp -a "$SRC" "$C" || exit 3
fi
rm -f "$C/postmaster.pid"
hba=(); [[ -f "$C/pg_hba.conf" ]] && hba=(--hba "$C/pg_hba.conf")
GOOPG_PGSHAPED_DP_TRACE=${DPTRACE:-} GOOPG_GEQO_TRACE=1 GOOPG_PG_SORT_RELATION_BYTES_COST=${PGSORT:-} GOOPG_GEQO_TRACE_EVAL=${GOOPG_GEQO_TRACE_EVAL:--1} GOOPG_CG_UNIT="$UNIT" setsid nohup "${REPO}/scripts/goopg-test-run.sh" "$BIN" start -D "$C" \
   --listen 127.0.0.1:5598 "${hba[@]}" >>"${REPO}/tmp/m0231/server1.log" 2>&1 < /dev/null &
for _ in $(seq 1 240); do pg_isready -h 127.0.0.1 -p 5598 -U postgres -q && break; sleep 1; done
pg_isready -h 127.0.0.1 -p 5598 -U postgres -q || { echo FATAL not ready; exit 5; }
echo ready
