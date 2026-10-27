#!/usr/bin/env bash
# usage: h-instr.sh <bin> <query-file> <runs> — user-mode instructions per TPC-H query on tmp/q17-clone
# (the twin of ds-instr.sh; same env as tpch-gap.sh). Dump query texts with tpch-dump-queries.go.txt.
set -u; cd /home/ryo/work/goopg/goopg; export PATH=$PWD/postgres/local_install/bin:$PATH PGPASSWORD=tpch
BIN=$1; F=$2; N=$3; D=tmp/q17-clone; PORT=5591
export GOMEMLIMIT=12GiB GOGC=off GOOPG_ANALYZE_SEED=20260905 GOOPG_MEM_HIGH=20G GOOPG_MEM_MAX=24G GOOPG_MEM_SWAP_MAX=0
"$BIN" stop -D $D >/dev/null 2>&1; sleep 1
(GOOPG_CG_UNIT=h-instr scripts/goopg-test-run.sh $BIN start -D $D --listen 127.0.0.1:$PORT --hba $D/pg_hba.conf > tmp/h-instr.server.log 2>&1 &)
for _ in $(seq 1 180); do pg_isready -h 127.0.0.1 -p $PORT -q 2>/dev/null && break; sleep 1; done
P=$(head -1 $D/postmaster.pid); run(){ psql -X -q -h 127.0.0.1 -p $PORT -U tpch -d tpch -f $F >/dev/null; }
run; run
perf stat -e instructions:u -x, -o /tmp/h-instr.stat -p $P & PS=$!; sleep 0.5
for i in $(seq 1 $N); do run; done
kill -INT $PS; wait $PS 2>/dev/null
awk -F, -v n=$N '/instructions/{printf "Ginstr/q=%.3f\n", $1/n/1e9}' /tmp/h-instr.stat
"$BIN" stop -D $D >/dev/null 2>&1
