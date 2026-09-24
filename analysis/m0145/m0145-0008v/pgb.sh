#!/usr/bin/env bash
set -u
R=/home/ryo/work/goopg/goopg; cd $R
export PATH=$R/postgres/local_install/bin:$PATH LD_LIBRARY_PATH=$R/postgres/local_install/lib
for arm in cand head cand head; do
  B=$R/tmp/m0145-0008v/goopg-$arm; D=$R/tmp/m0145-0008v/data-$arm
  rm -rf $D; $B init -D $D >/dev/null 2>&1
  GOOPG_CG_UNIT=m0145-0008v-$arm $R/scripts/goopg-test-run.sh $B start -D $D --listen 127.0.0.1:5534 > $R/tmp/m0145-0008v/srv-$arm.log 2>&1 &
  for i in $(seq 1 60); do pg_isready -h 127.0.0.1 -p 5534 -q && break; sleep 1; done
  pgbench -h 127.0.0.1 -p 5534 -U postgres -i -s 2 postgres >/dev/null 2>&1
  tps=$(pgbench -h 127.0.0.1 -p 5534 -U postgres -c 8 -j 4 -T 30 postgres 2>&1 | grep -E '^tps|failed' | tr '\n' ' ')
  sz=$(for t in pgbench_branches pgbench_tellers pgbench_accounts; do psql -h 127.0.0.1 -p 5534 -U postgres -d postgres -X -At -c "select '$t', max((ctid::text::point)[0]) from $t" 2>&1; done | tr '
' ' ')
  echo "$arm $tps pages: $sz"
  timeout 60 $B stop -D $D >/dev/null 2>&1
  systemctl --user stop m0145-0008v-$arm.scope >/dev/null 2>&1; systemctl --user reset-failed m0145-0008v-$arm.scope >/dev/null 2>&1
  rm -rf $D; sleep 2
done
