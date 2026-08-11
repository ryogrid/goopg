#!/bin/bash
# M0131-S31 minimal deterministic probe: does a SINGLE plain UPDATE make the row
# index-unreachable?  No concurrency, no pgbench, no crash.
set -u
REPO=/home/ryo/work/goopg/goopg
export PATH=$REPO/postgres/local_install/bin:$PATH
export LD_LIBRARY_PATH=$REPO/postgres/local_install/lib:${LD_LIBRARY_PATH:-}
DIR=/tmp/idxprobe2/data; PORT=5534; W=/tmp/idxprobe2
rm -rf $W && mkdir -p $W
Q(){ psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -At -c "$1"; }
$REPO/bin/goopg init -D $DIR --no-sync >$W/init.log 2>&1 || { echo INIT_FAIL; tail -3 $W/init.log; exit 1; }
GOOPG_CG_UNIT=idxprobe2 $REPO/scripts/goopg-test-run.sh $REPO/bin/goopg start -D $DIR --listen 127.0.0.1:$PORT >$W/server.log 2>&1 &
for i in $(seq 90); do Q 'select 1' >/dev/null 2>&1 && break; sleep 1; done
Q 'create table t(id int primary key, v int)' >/dev/null
Q 'insert into t select g, 0 from generate_series(1,2000) g' >/dev/null
echo "before: unreachable=$(Q 'select count(*) from generate_series(1,2000) g where not exists (select 1 from t where t.id=g)')"
Q 'update t set v=v+1 where id=7' >/dev/null
echo "after 1 update of id=7:"
echo "  index  id=7 : [$(Q 'select id,v from t where id=7')]"
echo "  heap   id=7 : [$(Q 'select id,v from t where id+0=7')]"
echo "  unreachable : $(Q 'select count(*) from generate_series(1,2000) g where not exists (select 1 from t where t.id=g)')"
# now update every id in 100..199 once, serially
for i in $(seq 100 199); do Q "update t set v=v+1 where id=$i" >/dev/null; done
echo "after 100 serial single-row updates (ids 100..199):"
echo "  unreachable total: $(Q 'select count(*) from generate_series(1,2000) g where not exists (select 1 from t where t.id=g)')"
echo "  unreachable in 100..199: $(Q 'select count(*) from generate_series(100,199) g where not exists (select 1 from t where t.id=g)')"
echo "  updated-but-reachable v>0 via heap: $(Q 'select count(*) from t where v+0>0')"
$REPO/bin/goopg stop -D $DIR >/dev/null 2>&1
