#!/bin/bash
set -u
REPO=/home/ryo/work/goopg/goopg
export PATH=$REPO/postgres/local_install/bin:$PATH
export LD_LIBRARY_PATH=$REPO/postgres/local_install/lib:${LD_LIBRARY_PATH:-}
DIR=/tmp/idxprobe/data; PORT=5534; W=/tmp/idxprobe
rm -rf $W && mkdir -p $W
Q(){ psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -At -c "$1"; }
$REPO/bin/goopg init -D $DIR --no-sync >$W/init.log 2>&1 || { echo INIT_FAIL; tail -3 $W/init.log; exit 1; }
GOOPG_CG_UNIT=idxprobe $REPO/scripts/goopg-test-run.sh $REPO/bin/goopg start -D $DIR --listen 127.0.0.1:$PORT >$W/server.log 2>&1 &
echo $! > $W/wrapper.pid
for i in $(seq 90); do Q 'select 1' >/dev/null 2>&1 && break; sleep 1; done
Q 'create table t(id int primary key, v int)' >/dev/null
Q 'insert into t select g, 0 from generate_series(1,20000) g' >/dev/null
echo "rows=$(Q 'select count(*) from t')"
cat > $W/upd.sql <<'SQL'
\set id random(1, 20000)
UPDATE t SET v = v + 1 WHERE id = :id;
SQL
pgbench -h 127.0.0.1 -p $PORT -U postgres -n -f $W/upd.sql -c ${CLIENTS:-4} -j 2 -T ${SECS:-20} postgres >$W/run.log 2>&1
grep -E "^tps|number of failed" $W/run.log | head -3
echo "heap count      : $(Q 'select count(*) from t')"
echo "heap distinct id: $(Q 'select count(distinct id) from t')"
echo "index-unreachable ids: $(Q 'select count(*) from generate_series(1,20000) g where not exists (select 1 from t where t.id=g)')"
Q 'select g from generate_series(1,20000) g where not exists (select 1 from t where t.id=g) order by g limit 5' > $W/bad.txt
echo "sample bad ids:"; cat $W/bad.txt
B=$(head -1 $W/bad.txt)
if [ -n "$B" ]; then
  echo "--- id=$B via index:"; Q "select id, v from t where id=$B"
  echo "--- id=$B via heap :"; Q "select id, v from t where id+0=$B"
fi
