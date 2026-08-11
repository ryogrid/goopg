#!/bin/bash
# M0131-S31 probe 3: same-session repeated updates (no pgbench, no concurrency).
set -u
REPO=/home/ryo/work/goopg/goopg
export PATH=$REPO/postgres/local_install/bin:$PATH
export LD_LIBRARY_PATH=$REPO/postgres/local_install/lib:${LD_LIBRARY_PATH:-}
DIR=/tmp/idxprobe3b/data; PORT=5546; W=/tmp/idxprobe3b
rm -rf $W && mkdir -p $W
Q(){ psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -At -c "$1"; }
$REPO/bin/goopg init -D $DIR --no-sync >$W/init.log 2>&1 || { echo INIT_FAIL; tail -3 $W/init.log; exit 1; }
GOOPG_CG_UNIT=idxprobe3b $REPO/scripts/goopg-test-run.sh $REPO/bin/goopg start -D $DIR --listen 127.0.0.1:$PORT >$W/server.log 2>&1 &
for i in $(seq 90); do Q 'select 1' >/dev/null 2>&1 && break; sleep 1; done
Q 'create table t(id int primary key, v int)' >/dev/null
Q 'insert into t select g, 0 from generate_series(1,20000) g' >/dev/null
# ONE session, N autocommit updates over random ids
python3 - "$PORT" > $W/upd.sql <<'PY'
import random,sys
random.seed(42)
for _ in range(20000):
    print(f"UPDATE t SET v=v+1 WHERE id={random.randint(1,20000)};")
PY
psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -q -f $W/upd.sql > $W/upd.log 2>&1
echo "update errors: $(grep -c ERROR $W/upd.log)"
echo "heap count      : $(Q 'select count(*) from t')"
echo "unreachable ids : $(Q 'select count(*) from generate_series(1,20000) g where not exists (select 1 from t where t.id=g)')"
echo "updated (v>0)   : $(Q 'select count(*) from t where v+0>0')"
Q 'select g from generate_series(1,20000) g where not exists (select 1 from t where t.id=g) order by g limit 5' > $W/bad.txt
echo "sample bad: $(tr "\n" " " < $W/bad.txt)"
B=$(head -1 $W/bad.txt)
[ -n "$B" ] && echo "bad row via heap: [$(Q "select id,v from t where id+0=$B")]"
$REPO/bin/goopg stop -D $DIR >/dev/null 2>&1
