#!/usr/bin/env bash
# usage: tmp/ds-planinstr.sh <bin> <q> <runs> — instructions:u per plan-only EXPLAIN (plan cache busted)
set -u; cd /home/ryo/work/goopg/goopg; source bench/tpcds/env_tpcds.sh; export PATH=$PWD/postgres/local_install/bin:$PATH
BIN=$1; Q=$2; N=$3; D=tmp/q64-clone
"$BIN" stop -D $D >/dev/null 2>&1; sleep 1
(GOOPG_CG_UNIT=ds-plan scripts/goopg-test-run.sh $BIN start -D $D --listen 127.0.0.1:5592 --hba $D/pg_hba.conf > tmp/ds-plan.server.log 2>&1 &)
for _ in $(seq 1 120); do pg_isready -h 127.0.0.1 -p 5592 -q 2>/dev/null && break; sleep 1; done
P=$(head -1 $D/postmaster.pid)
run(){ (echo "set work_mem='$((524288+$1))kB';"; echo "explain (costs off)"; cat $TPCDS_QUERY_DIR/query$Q.sql) | psql -X -q -h 127.0.0.1 -p 5592 -U $TPCDS_SUPERUSER -d postgres > /tmp/ds-planinstr.out; }
run 1000; run 1001
perf stat -e instructions:u -x, -o /tmp/ds-planinstr.stat -p $P & PS=$!; sleep 0.5
s=$(date +%s%N); for i in $(seq 1 $N); do run $i; done; e=$(date +%s%N)
kill -INT $PS; wait $PS 2>/dev/null
awk -F, -v n=$N -v ms=$(( (e-s)/1000000/N )) '/instructions/{printf "Ginstr/plan=%.3f wall/plan=%dms\n", $1/n/1e9, ms}' /tmp/ds-planinstr.stat
"$BIN" stop -D $D >/dev/null 2>&1
