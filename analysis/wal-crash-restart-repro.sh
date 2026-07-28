#!/bin/bash
# Crash-recovery probe: heavy concurrent write load, SIGKILL, restart.
set -u
REPO=/home/ryo/work/goopg/goopg
export PATH=$REPO/postgres/local_install/bin:$PATH
export LD_LIBRARY_PATH=$REPO/postgres/local_install/lib:${LD_LIBRARY_PATH:-}
DIR=/tmp/crashprobe/data
PORT=5533
LOG=/tmp/crashprobe/server.log
rm -rf /tmp/crashprobe && mkdir -p /tmp/crashprobe
$REPO/bin/goopg init -D $DIR --no-sync >/tmp/crashprobe/init.log 2>&1 || { echo INIT_FAIL; tail -5 /tmp/crashprobe/init.log; exit 1; }
GOOPG_CG_UNIT=crashprobe $REPO/scripts/goopg-test-run.sh $REPO/bin/goopg start -D $DIR --listen 127.0.0.1:$PORT >>$LOG 2>&1 &
for i in $(seq 60); do psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -c 'select 1' >/dev/null 2>&1 && break; sleep 1; done
echo "server up"
pgbench -h 127.0.0.1 -p $PORT -U postgres -i -s 5 postgres >/tmp/crashprobe/init_pgbench.log 2>&1 || { echo PGBENCH_INIT_FAIL; tail -5 /tmp/crashprobe/init_pgbench.log; }
pgbench -h 127.0.0.1 -p $PORT -U postgres -c 16 -j 4 -T ${LOADSEC:-45} postgres >/tmp/crashprobe/run.log 2>&1 &
PB=$!
sleep ${KILLAT:-30}
PIDS=$(pgrep -f "bin/goopg start -D $DIR")
echo "killing: $PIDS"
kill -9 $PIDS 2>/dev/null
wait $PB 2>/dev/null
sleep 2
ls -la $DIR/pg_wal | head -5; du -sh $DIR/pg_wal
echo "=== restart"
GOOPG_CG_UNIT=crashprobe2 $REPO/scripts/goopg-test-run.sh $REPO/bin/goopg start -D $DIR --listen 127.0.0.1:$PORT >/tmp/crashprobe/restart.log 2>&1 &
for i in $(seq 60); do psql -h 127.0.0.1 -p $PORT -U postgres -d postgres -c 'select 1' >/dev/null 2>&1 && { echo RESTART_OK; break; }; sleep 1; done
grep -i "wal replay\|panic\|exit status" /tmp/crashprobe/restart.log | head -5
