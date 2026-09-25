#!/usr/bin/env bash
set -u
R=/home/ryo/work/goopg/goopg; cd $R
export PATH=$R/postgres/local_install/bin:$PATH LD_LIBRARY_PATH=$R/postgres/local_install/lib
export GOMEMLIMIT=12GiB GOGC=100 GOOPG_MEM_HIGH=20G GOOPG_MEM_MAX=24G
D=$R/tmp/m0145-0008p/data
for arm in head cand head cand; do
  B=$R/tmp/m0145-0008p/goopg-$arm
  GOOPG_CG_UNIT=m0145-0008p-$arm $R/scripts/goopg-test-run.sh $B start -D $D --listen 127.0.0.1:5534 --hba $D/pg_hba.conf > $R/tmp/m0145-0008p/srv-$arm.log 2>&1 &
  for i in $(seq 1 120); do pg_isready -h 127.0.0.1 -p 5534 -q && break; sleep 1; done
  for w in 0 2; do
    for q in "count(*)" "sum(l_quantity)"; do
      out=$(psql -h 127.0.0.1 -p 5534 -U postgres -d tpch -X -At <<SQL 2>&1
SET max_parallel_workers_per_gather=$w;
\timing on
SELECT $q FROM lineitem;
SELECT $q FROM lineitem;
SELECT $q FROM lineitem;
SELECT $q FROM lineitem;
SQL
)
      ms=$(echo "$out" | grep -o 'Time: [0-9.]*' | awk '{printf "%d ", $2}')
      val=$(echo "$out" | grep -v 'Time\|SET' | sort -u | tr '\n' ' ')
      echo "$arm workers=$w $q  $ms  val=$val"
    done
  done
  timeout 60 $B stop -D $D >/dev/null 2>&1
  systemctl --user stop m0145-0008p-$arm.scope >/dev/null 2>&1; systemctl --user reset-failed m0145-0008p-$arm.scope >/dev/null 2>&1
  sleep 2
done
