#!/usr/bin/env bash
# usage: tmp/ds-alloc.sh <bin> <q|file> <runs> — Go-heap TotalAlloc / Mallocs per query (MemStats delta)
set -u; cd /home/ryo/work/goopg/goopg; source bench/tpcds/env_tpcds.sh; export PATH=$PWD/postgres/local_install/bin:$PATH
BIN=$1; Q=$2; N=$3; D=tmp/q64-clone; F=$TPCDS_QUERY_DIR/query$Q.sql; [[ -f $Q ]] && F=$Q
"$BIN" stop -D $D >/dev/null 2>&1; sleep 1
(GOOPG_PPROF_ADDR=127.0.0.1:6177 GOOPG_CG_UNIT=ds-alloc scripts/goopg-test-run.sh $BIN start -D $D --listen 127.0.0.1:5592 --hba $D/pg_hba.conf > tmp/ds-alloc.server.log 2>&1 &)
for _ in $(seq 1 120); do pg_isready -h 127.0.0.1 -p 5592 -q 2>/dev/null && break; sleep 1; done
run(){ psql -X -q -h 127.0.0.1 -p 5592 -U $TPCDS_SUPERUSER -d postgres -f $F >/dev/null; }
st(){ curl -s "http://127.0.0.1:6177/debug/pprof/heap?debug=1" | awk '/^# TotalAlloc/{a=$4} /^# Mallocs/{m=$4} END{print a, m}'; }
run; read a0 m0 < <(st); for i in $(seq 1 $N); do run; done; read a1 m1 < <(st)
awk -v a0=$a0 -v a1=$a1 -v m0=$m0 -v m1=$m1 -v n=$N 'BEGIN{printf "MB/q=%.1f Mallocs/q=%.0f\n", (a1-a0)/n/1048576, (m1-m0)/n}'
"$BIN" stop -D $D >/dev/null 2>&1
