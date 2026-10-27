#!/usr/bin/env bash
# usage: tmp/ds-warm.sh <bin> <warm-query-file-or-q> <warmruns> <q...> — warm the server past its heap plateau, then
# time each q (3 runs, wall + /proc user/sys CPU) on the same warm server
set -u; cd /home/ryo/work/goopg/goopg; source bench/tpcds/env_tpcds.sh; export PATH=$PWD/postgres/local_install/bin:$PATH
BIN=$1; WQ=$2; W=$3; shift 3; D=tmp/q64-clone
f(){ local q=$1; [[ -f $q ]] && echo $q || echo $TPCDS_QUERY_DIR/query$q.sql; }
"$BIN" stop -D $D >/dev/null 2>&1; sleep 1
(GOOPG_CG_UNIT=ds-warm scripts/goopg-test-run.sh $BIN start -D $D --listen 127.0.0.1:5592 --hba $D/pg_hba.conf > tmp/ds-warm.server.log 2>&1 &)
for _ in $(seq 1 120); do pg_isready -h 127.0.0.1 -p 5592 -q 2>/dev/null && break; sleep 1; done
P=$(head -1 $D/postmaster.pid); run(){ psql -X -q -h 127.0.0.1 -p 5592 -U $TPCDS_SUPERUSER -d postgres -f $(f $1) >/dev/null; }
for i in $(seq 1 $W); do run $WQ; done
for q in "$@"; do run $q; read u0 s0 < <(awk '{print $14, $15}' /proc/$P/stat); s=$(date +%s.%N); for i in 1 2 3; do run $q; done; e=$(date +%s.%N); read u1 s1 < <(awk '{print $14, $15}' /proc/$P/stat)
  echo "Q$q wall/q=$(echo "scale=3;($e-$s)/3"|bc) user/q=$(echo "scale=2;($u1-$u0)/300"|bc) sys/q=$(echo "scale=2;($s1-$s0)/300"|bc)"; done
"$BIN" stop -D $D >/dev/null 2>&1
