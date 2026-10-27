#!/usr/bin/env bash
# tmp/g1-split.sh <bin> <ds|h> <q...> — per query: planning ms (EXPLAIN SUMMARY ON, min of 2, first statement)
# and Go-heap MB / mallocs per run (pprof MemStats delta over 2 runs), one warm server
set -u; cd /home/ryo/work/goopg/goopg; source bench/tpcds/env_tpcds.sh; export PATH=$PWD/postgres/local_install/bin:$PATH
BIN=$1; K=$2; shift 2
if [[ $K == ds ]]; then D=tmp/q64-clone; PORT=5592; U=$TPCDS_SUPERUSER; DB=postgres; QF(){ echo $TPCDS_QUERY_DIR/query$1.sql; }; EXTRA="--hba $D/pg_hba.conf"
else D=tmp/q17-clone; PORT=5591; U=tpch; DB=tpch; export PGPASSWORD=tpch GOMEMLIMIT=12GiB GOGC=off GOOPG_ANALYZE_SEED=20260905; QF(){ echo tmp/g1-tpch-q$1.sql; }; EXTRA="--hba $D/pg_hba.conf"; fi
"$BIN" stop -D $D >/dev/null 2>&1; sleep 1
(GOOPG_PPROF_ADDR=127.0.0.1:6178 GOOPG_CG_UNIT=g1-split scripts/goopg-test-run.sh $BIN start -D $D --listen 127.0.0.1:$PORT $EXTRA > tmp/g1-split.server.log 2>&1 &)
for _ in $(seq 1 180); do pg_isready -h 127.0.0.1 -p $PORT -q 2>/dev/null && break; sleep 1; done
P(){ psql -X -q -h 127.0.0.1 -p $PORT -U $U -d $DB "$@"; }
st(){ curl -s "http://127.0.0.1:6178/debug/pprof/heap?debug=1" | awk '/^# TotalAlloc/{a=$4} /^# Mallocs/{m=$4} END{print a, m}'; }
for q in "$@"; do
  f=$(QF $q); P -f $f >/dev/null 2>&1
  pl=$(for i in 1 2; do (echo "explain (summary on, costs off)"; sed -n '1,/;/p' $f) | P 2>/dev/null | sed -n 's/.*Planning Time: \([0-9.]*\) ms.*/\1/p'; done | sort -n | head -1)
  read a0 m0 < <(st); P -f $f >/dev/null 2>&1; P -f $f >/dev/null 2>&1; read a1 m1 < <(st)
  awk -v q=$q -v pl=$pl -v a0=$a0 -v a1=$a1 -v m0=$m0 -v m1=$m1 'BEGIN{printf "Q%s\tplanning_ms=%s\tMB/run=%.1f\tMallocs/run=%.0f\n", q, pl, (a1-a0)/2/1048576, (m1-m0)/2}'
done
"$BIN" stop -D $D >/dev/null 2>&1
