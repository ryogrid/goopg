#!/usr/bin/env bash
# usage: tmp/ds-instr.sh <bin> <q|file> <runs> — user-mode instructions per query (load-insensitive)
set -u; cd /home/ryo/work/goopg/goopg; source bench/tpcds/env_tpcds.sh; export PATH=$PWD/postgres/local_install/bin:$PATH
BIN=$1; Q=$2; N=$3; D=tmp/q64-clone; F=$TPCDS_QUERY_DIR/query$Q.sql; [[ -f $Q ]] && F=$Q
"$BIN" stop -D $D >/dev/null 2>&1; sleep 1
(GOOPG_CG_UNIT=ds-instr scripts/goopg-test-run.sh $BIN start -D $D --listen 127.0.0.1:5592 --hba $D/pg_hba.conf > tmp/ds-instr.server.log 2>&1 &)
for _ in $(seq 1 120); do pg_isready -h 127.0.0.1 -p 5592 -q 2>/dev/null && break; sleep 1; done
P=$(head -1 $D/postmaster.pid); run(){ psql -X -q -h 127.0.0.1 -p 5592 -U $TPCDS_SUPERUSER -d postgres -f $F >/dev/null; }
run; run
perf stat -e instructions:u -x, -o /tmp/ds-instr.stat -p $P & PS=$!; sleep 0.5
for i in $(seq 1 $N); do run; done
kill -INT $PS; wait $PS 2>/dev/null
awk -F, -v n=$N '/instructions/{printf "Ginstr/q=%.3f\n", $1/n/1e9}' /tmp/ds-instr.stat
"$BIN" stop -D $D >/dev/null 2>&1
