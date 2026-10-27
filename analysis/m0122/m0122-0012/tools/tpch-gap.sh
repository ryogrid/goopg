#!/usr/bin/env bash
# One-off per-query warm timing on the q17 clone (tmp/, not committed).
set -u
REPO_ROOT=/home/ryo/work/goopg/goopg; cd "$REPO_ROOT"
BIN="$1"; LABEL="$2"; Q="${3:-1,2,3,4,5,6,7,8,9,10,11,12,13,14,16,17,18,19,20,21,22}"
PGDATA=$REPO_ROOT/tmp/q17-clone; PORT=5591; HOST=127.0.0.1
export GOMEMLIMIT=12GiB GOGC=off GOOPG_ANALYZE_SEED=20260905 GOOPG_MEM_HIGH=20G GOOPG_MEM_MAX=24G GOOPG_MEM_SWAP_MAX=0
export PATH=$REPO_ROOT/postgres/local_install/bin:$PATH
"$BIN" stop -D "$PGDATA" >/dev/null 2>&1
GOOPG_PPROF_ADDR=127.0.0.1:6291 GOOPG_CG_UNIT=gap-$LABEL scripts/goopg-test-run.sh "$BIN" start -D "$PGDATA" --listen $HOST:$PORT --hba "$PGDATA/pg_hba.conf" > tmp/gap-$LABEL.server.log 2>&1 &
for _ in $(seq 1 180); do pg_isready -h $HOST -p $PORT -U postgres -q && break; sleep 1; done
for pass in 1 2; do
  tmp/tpch-acceptance-runner -host $HOST -port $PORT -db tpch -user tpch -password tpch -queries "$Q" 2>&1 | grep -o "Q[0-9]*: [A-Z]* elapsed=[0-9.]*s" > tmp/gap-$LABEL.pass$pass.txt
done
timeout 60 "$BIN" stop -D "$PGDATA" >/dev/null 2>&1
