#!/usr/bin/env bash
# replay.sh <bin> <sf025|sf1> <table>... : clone the shipped corpus, re-insert each table in heap order into a
# copy declared with the same inline primary key, print the copy's pkey pages next to the corpus pkey's.
set -u
REPO=/home/ryo/work/goopg/goopg
source "${REPO}/bench/tpcds/env_tpcds.sh"
export PATH="${REPO}/postgres/local_install/bin:${PATH}"
BIN=$1; SF=$2; shift 2
if [[ $SF == sf025 ]]; then SRC="${SF025_GOOPG_DATA}"; PORT=5599; TSV="${SF025_DATA_DIR}"; else SRC="${TPCDS_PGDATA}"; PORT=5598; TSV="${TPCDS_DATA_DIR}"; fi
C="${REPO}/tmp/m0216/data-$SF"; UNIT=goopg-m0216-$SF
[[ -f "${SRC}.HOLD" || -f "${SRC}/postmaster.pid" ]] && { echo "FATAL src"; exit 3; }
pg_isready -h 127.0.0.1 -p $PORT -q 2>/dev/null && { echo "FATAL port busy"; exit 3; }
rm -rf "$C"; cp -a "$SRC" "$C" || exit 3; rm -f "$C/postmaster.pid"
hba=(); [[ -f "$C/pg_hba.conf" ]] && hba=(--hba "$C/pg_hba.conf")
systemctl --user reset-failed "${UNIT}.scope" >/dev/null 2>&1 || true
GOOPG_CG_UNIT="$UNIT" setsid nohup "${REPO}/scripts/goopg-test-run.sh" "$BIN" start -D "$C" \
   --listen 127.0.0.1:$PORT "${hba[@]}" >"${REPO}/tmp/m0216/server-$SF.log" 2>&1 < /dev/null &
for _ in $(seq 1 300); do pg_isready -h 127.0.0.1 -p $PORT -U postgres -q && break; sleep 1; done
pg_isready -h 127.0.0.1 -p $PORT -U postgres -q || { echo FATAL not ready; exit 5; }
DB=$(psql -h 127.0.0.1 -p $PORT -U postgres -At -c "select datname from pg_database where datname like 'tpcds%' limit 1")
P="psql -h 127.0.0.1 -p $PORT -U postgres -d ${DB:-postgres} -X -v ON_ERROR_STOP=1 -At"
for t in "$@"; do
  ddl=$(awk -v t="$t" '$0 ~ "^create table "t"$" {p=1} p {print} p && /^\);/ {exit}' "$TPCDS_TOOLS/tpcds.sql" | sed "1s/create table $t/create table r_$t/")
  $P -c "$ddl" >/dev/null || exit 6
  $P -c "COPY r_$t FROM '${TSV}/${t}.tsv'" >/dev/null || exit 7
  [[ -n "${BLKDIR:-}" ]] && $P -c "select (ctid::text::point)[0]::int b, count(*) from r_$t group by 1 order by 1" > "${BLKDIR}/goblk-$SF-$t.txt"
  echo "$t heap=$($P -c "select pg_relation_size('r_${t}'::regclass)") corpus=$($P -c "select pg_relation_size('${t}_pkey'::regclass)/8192") replay=$($P -c "select pg_relation_size('r_${t}_pkey'::regclass)/8192")"
done
[[ -n "${KEEP:-}" ]] || "$BIN" stop -D "$C" >/dev/null 2>&1
